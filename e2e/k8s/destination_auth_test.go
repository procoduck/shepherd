//go:build e2ek8s

package k8s_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/k8s"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

// TestDestinationSecretAuth is #229 verified where it is consumed: a
// basic_secret destination, used by a wizard pipeline, makes a REAL Alloy
// collector send authenticated remote-write requests with the credentials
// from a Kubernetes Secret on its own cluster, while the served config names
// only the Secret.
//
// The unit and gate tests prove the rendered text is what was intended and
// that `alloy validate` accepts it. They cannot prove the part that only
// happens at runtime: `alloy validate` does not evaluate expressions, so it
// passes `username = remote.kubernetes.secret.x.data["username"]` (a secret
// where a string is required) and a typo'd key alike. Only a running
// collector reading a real Secret shows the auth block works.
//
// The collector runs with the narrowest RBAC the docs tell an operator to
// grant: a Role on the Secret's namespace with get/list/watch on secrets, and
// nothing cluster-wide.
func TestDestinationSecretAuth(t *testing.T) {
	var (
		f           *fixture
		api         *shepherdAPI
		orgID       string
		collectorID string
		alloyNS     string
	)
	const (
		release    = "shepherd-dauth"
		cluster    = "dest-auth-cluster"
		password   = "e2e-dest-auth-admin-pass"
		sinkName   = "dauth-sink"
		sinkPort   = 8080
		secretName = "sink-credentials"
		// Obviously fake fixture credentials: the sink only records them.
		writeUser = "e2e-writer"
		writePass = "e2e-dest-auth-not-a-real-password"
		alloyName = "alloy-dauth"
	)
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(writeUser+":"+writePass))

	feat := features.New("destination auth: a basic_secret destination's Secret reaches a real Alloy's remote-write requests").
		WithLabel("suite", "destinations").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f = newFixture(ctx, t, cfg, "dest-auth")
			alloyNS = f.ns + "-alloy"
			out := kubectlArgv(cfg, "-n", f.ns, "patch", "secret", chartSecretName, "--type=merge",
				"-p", fmt.Sprintf(`{"stringData":{"SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD":%q}}`, password))
			if !strings.Contains(out, "patched") {
				t.Fatalf("setting the bootstrap admin password: %s", out)
			}
			helmRun(t, cfg, f, "install", release, "--set replicas=1", "--set simulator.enabled=false")
			waitDeploymentAvailable(t, cfg, f.ns, release)

			// The sink stands in for Mimir: it records each request's path and
			// headers, so the test can read back the Authorization header.
			sink := receiverSinkPod(f.ns, sinkName)
			if err := cfg.Client().Resources().Create(ctx, sink); err != nil {
				t.Fatalf("creating sink pod: %v", err)
			}
			if err := cfg.Client().Resources().Create(ctx, receiverSinkService(f.ns, sinkName, sinkPort)); err != nil {
				t.Fatalf("creating sink service: %v", err)
			}
			if err := wait.For(conditions.New(cfg.Client().Resources()).PodReady(sink),
				wait.WithTimeout(2*time.Minute), wait.WithInterval(2*time.Second)); err != nil {
				t.Fatalf("sink pod never became ready: %v", err)
			}

			api = newShepherdAPI(t, cfg, f.ns, release)
			api.login(t, "admin", password)
			org, err := api.admin.CreateOrg(ctx, connect.NewRequest(&mgmtv1.CreateOrgRequest{
				Name: "dest-auth", DisplayName: "Destination auth", AdminGroupId: "dest-auth-admins",
			}))
			if err != nil {
				t.Fatalf("CreateOrg: %v", err)
			}
			orgID = org.Msg.GetId()
			return ctx
		}).
		Assess("a collector with namespace-scoped Secret RBAC registers with Shepherd",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				_ = kubectlArgv(cfg, "delete", "namespace", alloyNS, "--ignore-not-found", "--wait", "--timeout=2m")
				if err := cfg.Client().Resources().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: alloyNS}}); err != nil {
					t.Fatalf("creating %s: %v", alloyNS, err)
				}
				token, err := api.admin.CreateAgentToken(ctx, connect.NewRequest(&mgmtv1.CreateAgentTokenRequest{Name: "dest-auth"}))
				if err != nil {
					t.Fatalf("CreateAgentToken: %v", err)
				}
				alloyImage, err := readVersionsEnvValue("ALLOY_IMAGE")
				if err != nil {
					t.Fatal(err)
				}
				shepherdURL := fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", release, f.ns)
				for _, obj := range destAuthCollectorObjects(alloyNS, alloyName, alloyImage, shepherdURL, cluster,
					token.Msg.GetId(), token.Msg.GetSecret(), secretName, writeUser, writePass) {
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
							kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "--tail=60"))
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
					if c.GetCluster() == cluster && c.GetRole() == "metrics" {
						collectorID = c.GetId()
					}
				}
				if collectorID == "" {
					t.Fatalf("no metrics collector for %s: %v", cluster, collectors.Msg.GetItems())
				}
				return ctx
			}).
		Assess("a wizard pipeline to a basic_secret destination is served naming only the Secret, and the collector applies it",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				if _, err := api.dests.CreateDestination(ctx, connect.NewRequest(&mgmtv1.CreateDestinationRequest{
					OrgId: orgID, Name: "sink-basic", Type: "prometheus",
					Url:             fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/api/v1/push", sinkName, f.ns, sinkPort),
					AuthMode:        "basic_secret",
					SecretNamespace: alloyNS, SecretName: secretName,
				})); err != nil {
					t.Fatalf("CreateDestination: %v", err)
				}
				state, err := structpb.NewStruct(map[string]any{
					// Alloy's own metrics endpoint: something real to scrape,
					// so the remote write has samples to send.
					"scrape_url":        "localhost:12345",
					"job_name":          "dest-auth",
					"scrape_interval":   "10s",
					"logs_enabled":      false,
					"metrics_dest_name": "sink-basic",
					"cluster_pattern":   cluster,
					"role":              "metrics",
				})
				if err != nil {
					t.Fatal(err)
				}
				p, err := api.wizards.CommitWizard(ctx, connect.NewRequest(&mgmtv1.CommitWizardRequest{
					OrgId: orgID, Kind: "app-observability", Name: "dest-auth-pipe", State: state,
				}))
				if err != nil {
					t.Fatalf("CommitWizard: %v", err)
				}
				// EnablePipeline runs Stage 3 over the merged config.
				if _, err := api.pipelines.EnablePipeline(ctx, connect.NewRequest(&mgmtv1.EnablePipelineRequest{
					OrgId: orgID, Id: p.Msg.GetId(),
				})); err != nil {
					t.Fatalf("EnablePipeline: %v", err)
				}

				end := time.Now().Add(3 * time.Minute)
				var content, status string
				for time.Now().Before(end) {
					if sc, err := api.fleet.GetServedConfig(ctx, connect.NewRequest(&mgmtv1.GetServedConfigRequest{OrgId: orgID, Id: collectorID})); err == nil {
						content = sc.Msg.GetContent()
					}
					if c, err := api.fleet.GetCollector(ctx, connect.NewRequest(&mgmtv1.GetCollectorRequest{OrgId: orgID, Id: collectorID})); err == nil {
						status = c.Msg.GetRemoteConfigStatus()
					}
					if strings.Contains(content, `declare "pipe_dest_auth_pipe"`) && status == "APPLIED" {
						break
					}
					time.Sleep(5 * time.Second)
				}
				if status != "APPLIED" || !strings.Contains(content, `declare "pipe_dest_auth_pipe"`) {
					t.Fatalf("pipeline not applied after 3m (status %q)\n--- served ---\n%s\n--- alloy logs ---\n%s", status, content,
						kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "--tail=60"))
				}
				for _, want := range []string{
					`remote.kubernetes.secret "metrics_auth"`,
					`namespace = "` + alloyNS + `"`,
					`name      = "` + secretName + `"`,
					`.data["username"]`, `.data["password"]`,
				} {
					if !strings.Contains(content, want) {
						t.Fatalf("served config lacks %q:\n%s", want, content)
					}
				}
				// The credential itself never passes through Shepherd.
				if strings.Contains(content, writePass) || strings.Contains(content, writeUser) {
					t.Fatalf("the served config contains the Secret's values:\n%s", content)
				}
				return ctx
			}).
		Assess("the sink receives remote-write requests carrying the Secret's basic auth, and none without it",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				end := time.Now().Add(3 * time.Minute)
				var seen map[string]int
				for {
					seen = sinkPushAuth(cfg, f.ns, sinkName)
					if seen[wantAuth] > 0 {
						break
					}
					if time.Now().After(end) {
						t.Fatalf("no remote-write request with the expected Authorization within 3m; saw %v\n--- alloy logs ---\n%s",
							redactAuth(seen), kubectlArgv(cfg, "-n", alloyNS, "logs", "deploy/"+alloyName, "--tail=60"))
					}
					time.Sleep(5 * time.Second)
				}
				for auth, n := range seen {
					if auth != wantAuth {
						t.Fatalf("%d remote-write request(s) reached the sink with Authorization %q — every push must carry the Secret's credentials",
							n, redact(auth))
					}
				}
				t.Logf("sink saw %d authenticated remote-write request(s)", seen[wantAuth])
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

// destAuthCollectorObjects is one Alloy collector polling Shepherd, plus the
// destination's credential Secret and the namespace-scoped RBAC the docs tell
// an operator to grant for remote.kubernetes.secret (scripts/docs-content/
// destinations.html): a Role with get/list/watch on secrets in the Secret's
// namespace, bound to the collector's ServiceAccount — nothing cluster-wide.
func destAuthCollectorObjects(ns, name, image, shepherdURL, cluster, tokenID, tokenSecret,
	credSecret, user, pass string,
) []k8s.Object {
	labels := map[string]string{"app": name}
	replicas := int32(1)
	config := fmt.Sprintf(`remotecfg {
  url = %q
  basic_auth {
    username = sys.env("SHEPHERD_TOKEN_ID")
    password = sys.env("SHEPHERD_TOKEN_SECRET")
  }
  attributes = {
    cluster = %q,
    role    = "metrics",
  }
  poll_frequency = "10s"
}
`, shepherdURL, cluster)
	return []k8s.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: credSecret, Namespace: ns},
			StringData: map[string]string{"username": user, "password": pass},
		},
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
			ObjectMeta: metav1.ObjectMeta{Name: name + "-read-secrets", Namespace: ns},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "watch"},
			}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-read-secrets", Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name + "-read-secrets"},
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
						Containers: []corev1.Container{{
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
							},
						}},
						Volumes: []corev1.Volume{
							{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: name},
							}}},
							{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
						},
					},
				},
			},
		},
	}
}

// sinkPushAuth reads the sink's request log and counts remote-write pushes
// (POST /api/v1/push) by the Authorization header they carried; "" counts
// pushes with none.
func sinkPushAuth(cfg *envconf.Config, ns, sink string) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(kubectlArgv(cfg, "-n", ns, "logs", sink), "\n") {
		start := strings.Index(line, "{")
		if start < 0 {
			continue
		}
		var req struct {
			Path    string            `json:"path"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		}
		if json.Unmarshal([]byte(line[start:]), &req) != nil || req.Path != "/api/v1/push" {
			continue
		}
		counts[req.Headers["authorization"]]++
	}
	return counts
}

// redact keeps a failure message useful without printing a credential: the
// scheme and the length are enough to tell "missing" from "wrong".
func redact(auth string) string {
	if auth == "" {
		return ""
	}
	scheme, _, _ := strings.Cut(auth, " ")
	return fmt.Sprintf("%s <%d chars>", scheme, len(auth))
}

func redactAuth(seen map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range seen {
		out[redact(k)] += v
	}
	return out
}
