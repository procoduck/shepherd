//go:build e2ek8s

package k8s_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

// TestDestinationTenantTLS is #261 verified where it is consumed: wizard
// pipelines to destinations with a tenant and TLS options make a REAL Alloy
// collector
//
//   - trust a private CA read from a ConfigMap (under an overridden key) and
//     from a Secret (the default ca.crt),
//   - present a client certificate read from a kubernetes.io/tls Secret to a
//     sink that REQUIRES and verifies one,
//   - verify the server as the destination's server_name, when the URL's host
//     is not in the certificate,
//   - and send the destination's tenant as X-Scope-OrgID, on both
//     prometheus.remote_write and loki.write,
//
// while the served config names only the objects. A third destination whose
// CA ConfigMap holds a DIFFERENT CA delivers nothing, so the CA reference is
// what decides trust.
//
// `alloy validate` cannot prove this: it does not evaluate expressions, and it
// accepts a Secret value assigned straight to the string-typed ca_pem /
// cert_pem (checked while building #261). Only a running collector loading
// real objects shows the convert.nonsensitive wrapping and the key names are
// right.
//
// The sink is a small stdlib Go program run on the pinned GO_IMAGE rather than
// mendhak/http-https-echo: that image's mTLS mode requests a client
// certificate but does not reject a request without one, so it could not
// prove the certificate was presented.
func TestDestinationTenantTLS(t *testing.T) {
	var (
		f           *fixture
		api         *shepherdAPI
		orgID       string
		collectorID string
		alloyNS     string
		pki         destTLSPKI
	)
	const (
		release   = "shepherd-dtls"
		cluster   = "dest-tls-cluster"
		password  = "e2e-dest-tls-admin-pass"
		sinkName  = "dtls-sink"
		sinkPort  = 8443
		alloyName = "alloy-dtls"
		clientCN  = "e2e-collector"
		// The collector's TLS objects.
		caConfigMap     = "backend-trust"
		caKey           = "trust-bundle.pem"
		otherCAMap      = "other-trust"
		clientSecret    = "collector-mtls"
		promTenant      = "acme"
		lokiTenant      = "acme-logs"
		wrongCATenant   = "wrongca"
		logPath         = "/var/log/app/app.log"
		appPipeline     = "dest-tls-app"
		wrongCAPipeline = "dest-tls-wrong-ca"
	)

	feat := features.New("destination tenant and TLS: a real Alloy trusts a private CA, presents a client certificate and sends the tenant").
		WithLabel("suite", "destinations").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f = newFixture(ctx, t, cfg, "dest-tls")
			alloyNS = f.ns + "-alloy"
			out := kubectlArgv(cfg, "-n", f.ns, "patch", "secret", chartSecretName, "--type=merge",
				"-p", fmt.Sprintf(`{"stringData":{"SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD":%q}}`, password))
			if !strings.Contains(out, "patched") {
				t.Fatalf("setting the bootstrap admin password: %s", out)
			}
			helmRun(t, cfg, f, "install", release, "--set replicas=1", "--set simulator.enabled=false")
			waitDeploymentAvailable(t, cfg, f.ns, release)

			// The sink's certificate names only the Service's FQDN. The
			// prometheus destination's URL uses the short `<svc>.<ns>.svc`
			// form, which the certificate does not cover, so its pushes
			// verify only because the destination sets server_name.
			sinkFQDN := fmt.Sprintf("%s.%s.svc.cluster.local", sinkName, f.ns)
			pki = newDestTLSPKI(t, sinkFQDN, clientCN)

			goImage, err := readVersionsEnvValue("GO_IMAGE")
			if err != nil {
				t.Fatal(err)
			}
			for _, obj := range destTLSSinkObjects(f.ns, sinkName, goImage, sinkPort, pki) {
				if err := cfg.Client().Resources().Create(ctx, obj); err != nil {
					t.Fatalf("creating sink %T: %v", obj, err)
				}
			}
			sink := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sinkName, Namespace: f.ns}}
			// `go run` compiles first: give it time.
			if err := wait.For(conditions.New(cfg.Client().Resources()).PodReady(sink),
				wait.WithTimeout(4*time.Minute), wait.WithInterval(3*time.Second)); err != nil {
				t.Fatalf("sink pod never became ready: %v\n--- logs ---\n%s", err, kubectlArgv(cfg, "-n", f.ns, "logs", sinkName))
			}

			api = newShepherdAPI(t, cfg, f.ns, release)
			api.login(t, "admin", password)
			org, err := api.admin.CreateOrg(ctx, connect.NewRequest(&mgmtv1.CreateOrgRequest{
				Name: "dest-tls", DisplayName: "Destination TLS", AdminGroupId: "dest-tls-admins",
			}))
			if err != nil {
				t.Fatalf("CreateOrg: %v", err)
			}
			orgID = org.Msg.GetId()
			return ctx
		}).
		Assess("a collector with namespace-scoped Secret and ConfigMap RBAC registers with Shepherd",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				_ = kubectlArgv(cfg, "delete", "namespace", alloyNS, "--ignore-not-found", "--wait", "--timeout=2m")
				if err := cfg.Client().Resources().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: alloyNS}}); err != nil {
					t.Fatalf("creating %s: %v", alloyNS, err)
				}
				token, err := api.admin.CreateAgentToken(ctx, connect.NewRequest(&mgmtv1.CreateAgentTokenRequest{Name: "dest-tls"}))
				if err != nil {
					t.Fatalf("CreateAgentToken: %v", err)
				}
				alloyImage, err := readVersionsEnvValue("ALLOY_IMAGE")
				if err != nil {
					t.Fatal(err)
				}
				shepherdURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", release, f.ns)
				objs := destTLSCollectorObjects(alloyNS, alloyName, alloyImage, shepherdURL, cluster,
					token.Msg.GetId(), token.Msg.GetSecret(), logPath)
				objs = append(objs,
					// The trusted CA under a non-default key, as a trust-manager
					// Bundle would publish it.
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: caConfigMap, Namespace: alloyNS},
						Data:       map[string]string{caKey: string(pki.caPEM)},
					},
					// A different CA: the wrong-CA destination's trust.
					&corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{Name: otherCAMap, Namespace: alloyNS},
						Data:       map[string]string{"ca.crt": string(pki.otherCAPEM)},
					},
					// What cert-manager writes for a Certificate: tls.crt,
					// tls.key and ca.crt in a kubernetes.io/tls Secret.
					&corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{Name: clientSecret, Namespace: alloyNS},
						Type:       corev1.SecretTypeTLS,
						Data: map[string][]byte{
							"tls.crt": pki.clientCertPEM, "tls.key": pki.clientKeyPEM, "ca.crt": pki.caPEM,
						},
					},
				)
				for _, obj := range objs {
					if err := cfg.Client().Resources().Create(ctx, obj); err != nil {
						t.Fatalf("creating %T in %s: %v", obj, alloyNS, err)
					}
				}
				waitDeploymentAvailable(t, cfg, alloyNS, alloyName)

				end := time.Now().Add(4 * time.Minute)
				for {
					resp, err := api.admin.ListClusters(ctx, connect.NewRequest(&mgmtv1.ListClustersRequest{}))
					found := false
					if err == nil {
						for _, c := range resp.Msg.GetItems() {
							found = found || c.GetName() == cluster
						}
					}
					if found {
						break
					}
					if time.Now().After(end) {
						t.Fatalf("cluster %q never registered within 4m\n--- alloy logs ---\n%s", cluster,
							kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "-c", "alloy", "--tail=60"))
					}
					time.Sleep(5 * time.Second)
				}
				if _, err := api.admin.ClaimCluster(ctx, connect.NewRequest(&mgmtv1.ClaimClusterRequest{Cluster: cluster, OrgId: orgID})); err != nil {
					t.Fatalf("ClaimCluster: %v", err)
				}
				collectors, err := api.fleet.ListCollectors(ctx, connect.NewRequest(&mgmtv1.ListCollectorsRequest{OrgId: orgID}))
				if err != nil {
					t.Fatalf("ListCollectors: %v", err)
				}
				for _, c := range collectors.Msg.GetItems() {
					if c.GetCluster() == cluster && c.GetRole() == "singleton" {
						collectorID = c.GetId()
					}
				}
				if collectorID == "" {
					t.Fatalf("no singleton collector for %s: %v", cluster, collectors.Msg.GetItems())
				}
				return ctx
			}).
		Assess("wizard pipelines to tenant+TLS destinations are served naming only the objects, and the collector applies them",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				mustStruct := func(m map[string]any) *structpb.Struct {
					s, err := structpb.NewStruct(m)
					if err != nil {
						t.Fatal(err)
					}
					return s
				}
				clientRef := map[string]any{"namespace": alloyNS, "name": clientSecret}
				for _, d := range []*mgmtv1.CreateDestinationRequest{
					{
						OrgId: orgID, Name: "sink-prom", Type: "prometheus", AuthMode: "none", TenantId: promTenant,
						Url: fmt.Sprintf("https://%s.%s.svc:%d/api/v1/push", sinkName, f.ns, sinkPort),
						Extra: mustStruct(map[string]any{"tls": map[string]any{
							"ca":          map[string]any{"kind": "configmap", "namespace": alloyNS, "name": caConfigMap, "key": caKey},
							"client_cert": clientRef,
							"server_name": fmt.Sprintf("%s.%s.svc.cluster.local", sinkName, f.ns),
						}}),
					},
					{
						OrgId: orgID, Name: "sink-loki", Type: "loki", AuthMode: "none", TenantId: lokiTenant,
						Url: fmt.Sprintf("https://%s.%s.svc.cluster.local:%d/loki/api/v1/push", sinkName, f.ns, sinkPort),
						// CA and client certificate from the one cert-manager
						// Secret, the CA under the default ca.crt.
						Extra: mustStruct(map[string]any{"tls": map[string]any{
							"ca":          map[string]any{"kind": "secret", "namespace": alloyNS, "name": clientSecret},
							"client_cert": clientRef,
						}}),
					},
					{
						OrgId: orgID, Name: "sink-wrong-ca", Type: "prometheus", AuthMode: "none", TenantId: wrongCATenant,
						Url: fmt.Sprintf("https://%s.%s.svc.cluster.local:%d/api/v1/push", sinkName, f.ns, sinkPort),
						Extra: mustStruct(map[string]any{"tls": map[string]any{
							"ca":          map[string]any{"kind": "configmap", "namespace": alloyNS, "name": otherCAMap},
							"client_cert": clientRef,
						}}),
					},
				} {
					if _, err := api.dests.CreateDestination(ctx, connect.NewRequest(d)); err != nil {
						t.Fatalf("CreateDestination %s: %v", d.GetName(), err)
					}
				}

				commit := func(name string, state map[string]any) {
					p, err := api.wizards.CommitWizard(ctx, connect.NewRequest(&mgmtv1.CommitWizardRequest{
						OrgId: orgID, Kind: "app-observability", Name: name, State: mustStruct(state),
					}))
					if err != nil {
						t.Fatalf("CommitWizard %s: %v", name, err)
					}
					if _, err := api.pipelines.EnablePipeline(ctx, connect.NewRequest(&mgmtv1.EnablePipelineRequest{
						OrgId: orgID, Id: p.Msg.GetId(),
					})); err != nil {
						t.Fatalf("EnablePipeline %s: %v", name, err)
					}
				}
				// Alloy's own metrics endpoint is something real to scrape;
				// the sidecar appends to logPath every second.
				commit(appPipeline, map[string]any{
					"scrape_url": "localhost:12345", "job_name": "dest-tls", "scrape_interval": "10s",
					"metrics_dest_name": "sink-prom",
					"logs_enabled":      true, "log_path": logPath, "logs_dest_name": "sink-loki",
					"cluster_pattern": cluster, "role": "singleton",
				})
				commit(wrongCAPipeline, map[string]any{
					"scrape_url": "localhost:12345", "job_name": "dest-tls-wrong", "scrape_interval": "10s",
					"metrics_dest_name": "sink-wrong-ca", "logs_enabled": false,
					"cluster_pattern": cluster, "role": "singleton",
				})

				end := time.Now().Add(3 * time.Minute)
				var content, status string
				applied := func() bool {
					return status == "APPLIED" &&
						strings.Contains(content, `declare "pipe_dest_tls_app"`) &&
						strings.Contains(content, `declare "pipe_dest_tls_wrong_ca"`)
				}
				for time.Now().Before(end) {
					if sc, err := api.fleet.GetServedConfig(ctx, connect.NewRequest(&mgmtv1.GetServedConfigRequest{OrgId: orgID, Id: collectorID})); err == nil {
						content = sc.Msg.GetContent()
					}
					if c, err := api.fleet.GetCollector(ctx, connect.NewRequest(&mgmtv1.GetCollectorRequest{OrgId: orgID, Id: collectorID})); err == nil {
						status = c.Msg.GetRemoteConfigStatus()
					}
					if applied() {
						break
					}
					time.Sleep(5 * time.Second)
				}
				if !applied() {
					t.Fatalf("pipelines not applied after 3m (status %q)\n--- served ---\n%s\n--- alloy logs ---\n%s", status, content,
						kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "-c", "alloy", "--tail=80"))
				}
				for _, want := range []string{
					`remote.kubernetes.configmap "metrics_tls_ca"`,
					`.data["` + caKey + `"]`,
					`.data["tls.crt"]`, `.data["tls.key"]`, `.data["ca.crt"]`,
					`"X-Scope-OrgID" = "` + promTenant + `"`,
					`tenant_id = "` + lokiTenant + `"`,
					`server_name = "` + sinkName + "." + f.ns + `.svc.cluster.local"`,
				} {
					if !strings.Contains(content, want) {
						t.Fatalf("served config lacks %q:\n%s", want, content)
					}
				}
				// Certificate material never passes through Shepherd.
				if strings.Contains(content, "BEGIN") {
					t.Fatalf("the served config contains PEM material:\n%s", content)
				}
				return ctx
			}).
		Assess("the sink receives remote-write and Loki pushes with the tenant over verified mTLS, and nothing through the wrong CA",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				end := time.Now().Add(3 * time.Minute)
				var seen []destTLSSinkRequest
				count := func(path, tenant string) int {
					n := 0
					for _, r := range seen {
						if r.Path == path && r.Tenant == tenant {
							n++
						}
					}
					return n
				}
				for {
					seen = destTLSSinkRequests(cfg, f.ns, sinkName)
					if count("/api/v1/push", promTenant) > 0 && count("/loki/api/v1/push", lokiTenant) > 0 {
						break
					}
					if time.Now().After(end) {
						t.Fatalf("expected remote-write (tenant %s) and Loki (tenant %s) pushes within 3m; saw %v\n--- alloy logs ---\n%s",
							promTenant, lokiTenant, seen,
							kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "-c", "alloy", "--tail=80"))
					}
					time.Sleep(5 * time.Second)
				}
				for _, r := range seen {
					// The sink requires and verifies a client certificate,
					// so every logged request presented one; check it is the
					// collector's from the Secret.
					if r.CN != clientCN {
						t.Fatalf("a request reached the sink with client certificate CN %q, want %q: %+v", r.CN, clientCN, r)
					}
					switch r.Tenant {
					case promTenant, lokiTenant:
					default:
						t.Fatalf("a request reached the sink with tenant %q — the wrong-CA destination must deliver nothing, and every push must carry its tenant: %+v", r.Tenant, r)
					}
				}
				t.Logf("sink saw %d remote-write and %d Loki push(es)", count("/api/v1/push", promTenant), count("/loki/api/v1/push", lokiTenant))

				// The wrong-CA destination: the collector refuses the sink's
				// certificate. Its pushes failing is the point; the error must
				// say why.
				end = time.Now().Add(2 * time.Minute)
				for {
					logs := kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "-c", "alloy", "--tail=400")
					if strings.Contains(logs, "x509") && strings.Contains(logs, "unknown authority") {
						break
					}
					if time.Now().After(end) {
						t.Fatalf("no x509 unknown-authority error from the wrong-CA destination within 2m\n--- alloy logs ---\n%s", logs)
					}
					time.Sleep(5 * time.Second)
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if api != nil {
				api.close()
			}
			if !keepCluster() && alloyNS != "" {
				_ = kubectlArgv(cfg, "delete", "namespace", alloyNS, "--ignore-not-found", "--wait=false")
			}
			f.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// destTLSPKI is the test's private PKI: a CA, the sink's server certificate
// and the collector's client certificate issued by it, and an unrelated CA.
type destTLSPKI struct {
	caPEM, otherCAPEM           []byte
	serverCertPEM, serverKeyPEM []byte
	clientCertPEM, clientKeyPEM []byte
}

func newDestTLSPKI(t *testing.T, serverDNS, clientCN string) destTLSPKI {
	t.Helper()
	caCert, caKey, caPEM := destTLSCert(t, "e2e destination CA", nil, nil, nil, true)
	_, _, otherPEM := destTLSCert(t, "e2e unrelated CA", nil, nil, nil, true)
	serverCertPEM, serverKeyPEM := destTLSLeaf(t, "e2e sink", []string{serverDNS}, caCert, caKey, x509.ExtKeyUsageServerAuth)
	clientCertPEM, clientKeyPEM := destTLSLeaf(t, clientCN, nil, caCert, caKey, x509.ExtKeyUsageClientAuth)
	return destTLSPKI{
		caPEM: caPEM, otherCAPEM: otherPEM,
		serverCertPEM: serverCertPEM, serverKeyPEM: serverKeyPEM,
		clientCertPEM: clientCertPEM, clientKeyPEM: clientKeyPEM,
	}
}

func destTLSLeaf(t *testing.T, cn string, dns []string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	_, key, pemBytes := destTLSCert(t, cn, dns, ca, caKey, false, usage)
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pemBytes, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// destTLSCert issues a certificate: self-signed when parent is nil.
func destTLSCert(t *testing.T, cn string, dns []string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey,
	isCA bool, usage ...x509.ExtKeyUsage,
) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dns,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
	}
	if isCA {
		tmpl.IsCA = true
		tmpl.BasicConstraintsValid = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// destTLSSinkSource is the sink: HTTPS that REQUIRES a client certificate
// verified against the test CA, logging one JSON line per request with its
// path, X-Scope-OrgID and the client certificate's CN. A handshake without a
// valid client certificate never reaches the handler.
const destTLSSinkSource = `package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
)

func main() {
	pool := x509.NewCertPool()
	ca, err := os.ReadFile("/tls/ca.crt")
	if err != nil || !pool.AppendCertsFromPEM(ca) {
		log.Fatalf("reading the client CA: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	srv := &http.Server{
		Addr: ":8443",
		TLSConfig: &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			cn := ""
			if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				cn = r.TLS.PeerCertificates[0].Subject.CommonName
			}
			_ = enc.Encode(map[string]string{"path": r.URL.Path, "tenant": r.Header.Get("X-Scope-OrgID"), "cn": cn})
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	log.Fatal(srv.ListenAndServeTLS("/tls/tls.crt", "/tls/tls.key"))
}
`

// destTLSSinkObjects is the sink's source, server certificate, pod and Service.
func destTLSSinkObjects(ns, name, image string, port int32, pki destTLSPKI) []k8s.Object {
	labels := map[string]string{"app": name}
	return []k8s.Object{
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-src", Namespace: ns},
			Data:       map[string]string{"sink.go": destTLSSinkSource},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-tls", Namespace: ns},
			Data: map[string][]byte{
				"tls.crt": pki.serverCertPEM, "tls.key": pki.serverKeyPEM, "ca.crt": pki.caPEM,
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:    "sink",
					Image:   image,
					Command: []string{"go", "run", "/src/sink.go"},
					Env: []corev1.EnvVar{
						{Name: "GOCACHE", Value: "/tmp/gocache"},
						{Name: "GOTOOLCHAIN", Value: "local"},
						{Name: "HOME", Value: "/tmp"},
					},
					Ports: []corev1.ContainerPort{{ContainerPort: port}},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler:  corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}},
						PeriodSeconds: 2,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "src", MountPath: "/src", ReadOnly: true},
						{Name: "tls", MountPath: "/tls", ReadOnly: true},
						{Name: "tmp", MountPath: "/tmp"},
					},
				}},
				Volumes: []corev1.Volume{
					{Name: "src", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: name + "-src"},
					}}},
					{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name + "-tls"}}},
					{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				},
			},
		},
		receiverSinkService(ns, name, port),
	}
}

// destTLSSinkRequest is one line of the sink's request log.
type destTLSSinkRequest struct {
	Path   string `json:"path"`
	Tenant string `json:"tenant"`
	CN     string `json:"cn"`
}

func destTLSSinkRequests(cfg *envconf.Config, ns, sink string) []destTLSSinkRequest {
	var out []destTLSSinkRequest
	for _, line := range strings.Split(kubectlArgv(cfg, "-n", ns, "logs", sink), "\n") {
		var r destTLSSinkRequest
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// destTLSCollectorObjects is one Alloy collector (role singleton: it carries
// metrics and logs) polling Shepherd, a sidecar appending to logPath for
// loki.source.file, and the namespace-scoped RBAC the docs tell an operator to
// grant: get/list/watch on secrets AND configmaps (a CA in a ConfigMap) in the
// objects' namespace — nothing cluster-wide.
func destTLSCollectorObjects(ns, name, image, shepherdURL, cluster, tokenID, tokenSecret, logPath string) []k8s.Object {
	labels := map[string]string{"app": name}
	replicas := int32(1)
	logDir := logPath[:strings.LastIndex(logPath, "/")]
	config := fmt.Sprintf(`remotecfg {
  url = %q
  basic_auth {
    username = sys.env("SHEPHERD_TOKEN_ID")
    password = sys.env("SHEPHERD_TOKEN_SECRET")
  }
  attributes = {
    cluster = %q,
    role    = "singleton",
  }
  poll_frequency = "10s"
}
`, shepherdURL, cluster)
	logVolume := corev1.VolumeMount{Name: "applog", MountPath: logDir}
	return []k8s.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-agent-token", Namespace: ns},
			StringData: map[string]string{"id": tokenID, "secret": tokenSecret},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Data:       map[string]string{"config.alloy": config},
		},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-read-tls", Namespace: ns},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""}, Resources: []string{"secrets", "configmaps"}, Verbs: []string{"get", "list", "watch"},
			}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-read-tls", Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name + "-read-tls"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: ns}},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{
						ServiceAccountName: name,
						Containers: []corev1.Container{
							{
								Name:  "alloy",
								Image: image,
								Args: []string{
									"run", "/etc/alloy/config.alloy", "--storage.path=/tmp/alloy",
									"--server.http.listen-addr=0.0.0.0:12345", "--disable-reporting",
								},
								Env: []corev1.EnvVar{
									{Name: "SHEPHERD_TOKEN_ID", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: name + "-agent-token"}, Key: "id",
									}}},
									{Name: "SHEPHERD_TOKEN_SECRET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
										LocalObjectReference: corev1.LocalObjectReference{Name: name + "-agent-token"}, Key: "secret",
									}}},
								},
								VolumeMounts: []corev1.VolumeMount{
									{Name: "config", MountPath: "/etc/alloy", ReadOnly: true},
									{Name: "tmp", MountPath: "/tmp"},
									logVolume,
								},
							},
							{
								Name:         "applog",
								Image:        "busybox:1.36",
								Command:      []string{"sh", "-c", fmt.Sprintf(`i=0; while true; do i=$((i+1)); echo "level=info msg=e2e n=$i" >> %s; sleep 1; done`, logPath)},
								VolumeMounts: []corev1.VolumeMount{logVolume},
							},
						},
						Volumes: []corev1.Volume{
							{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: name},
							}}},
							{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
							{Name: "applog", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						},
					},
				},
			},
		},
	}
}
