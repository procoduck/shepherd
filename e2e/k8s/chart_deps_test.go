//go:build e2ek8s

package k8s_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// The chart's two optional dependencies, against the real operators.
//
// Everything else about these integrations can be checked by rendering:
// deploy/helm/chart_deps_test.go asserts the manifests have the right shape.
// What rendering CANNOT tell you is whether the shape is CORRECT -- whether
// CloudNativePG really publishes the key we read the database URL from,
// whether the External Secrets Password generator really produces something
// that survives our b64enc into the 32 bytes Shepherd demands, and whether the
// install actually converges when a migration Job races two controllers that
// are still creating the things it needs. Those are answered here or nowhere.
//
// The ordering race is the reason this file earns its runtime. On a first
// install the migration hook runs while CNPG is still initialising Postgres
// AND while ESO has not yet materialised the Secret holding the encryption
// key. Nothing sequences that: it converges only because the Job retries. A
// test that installed against a warm cluster would never see it.
const (
	cnpgNamespace = "cnpg-system"
	cnpgRelease   = "cnpg"
	cnpgChartRef  = "oci://ghcr.io/cloudnative-pg/charts/cloudnative-pg"

	esoNamespace = "external-secrets"
	esoRelease   = "external-secrets"
	esoChartRef  = "oci://ghcr.io/external-secrets/charts/external-secrets"

	operatorInstallTimeout = 5 * time.Minute
	// Generous next to the other features' 5m: this one waits for an operator
	// to provision a database from scratch, and for a migration Job to back
	// off and retry until it does.
	depsInstallTimeout = 10 * time.Minute
)

// installCNPGOperator installs CloudNativePG, pinned from deploy/versions.env
// the same way the Gateway API CRDs are -- one place to bump, and the suite
// cannot silently start testing against a different version than the pin says.
func installCNPGOperator(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	version, err := readVersionsEnvValue("CNPG_CHART_VERSION")
	if err != nil {
		return ctx, err
	}
	log.Printf("installing CloudNativePG %s (database operator under test)", version)
	cmd := fmt.Sprintf(
		"helm install %s %s --version %s --kubeconfig %s -n %s --create-namespace --wait --timeout %s",
		cnpgRelease, cnpgChartRef, version, cfg.KubeconfigFile(), cnpgNamespace, operatorInstallTimeout,
	)
	if p := utils.RunCommand(cmd); p.Err() != nil {
		return ctx, fmt.Errorf("installing cloudnative-pg %s: %w: %s", version, p.Err(), p.Result())
	}
	return ctx, nil
}

// installExternalSecretsOperator installs ESO and its CRDs, including the
// Password generator the chart's generated-secrets path depends on.
func installExternalSecretsOperator(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	version, err := readVersionsEnvValue("EXTERNAL_SECRETS_CHART_VERSION")
	if err != nil {
		return ctx, err
	}
	log.Printf("installing External Secrets Operator %s (secret generator under test)", version)
	cmd := fmt.Sprintf(
		"helm install %s %s --version %s --kubeconfig %s -n %s --create-namespace --wait --timeout %s",
		esoRelease, esoChartRef, version, cfg.KubeconfigFile(), esoNamespace, operatorInstallTimeout,
	)
	if p := utils.RunCommand(cmd); p.Err() != nil {
		return ctx, fmt.Errorf("installing external-secrets %s: %w: %s", version, p.Err(), p.Result())
	}
	return ctx, nil
}

// TestChartProvisionsItsOwnDependencies installs Shepherd supplying NOTHING:
// no database, no connection string, no encryption key, no admin password.
// That is the install the docs recommend to somebody starting from an empty
// cluster, and until now nothing had ever run it.
func TestChartProvisionsItsOwnDependencies(t *testing.T) {
	const (
		release     = "shepherd"
		clusterName = release + "-db"
		secretName  = release + "-secrets"
	)
	ns := "shepherd-deps"

	feat := features.New("chart provisions its own database and secrets").
		WithLabel("suite", "chart").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// A bare namespace: deliberately no Secret and no database, unlike
			// newFixture. Supplying either would defeat the point.
			_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait --timeout=3m",
				cfg.KubeconfigFile(), ns))
			if p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s create namespace %s",
				cfg.KubeconfigFile(), ns)); p.Err() != nil {
				t.Fatalf("creating namespace %s: %v: %s", ns, p.Err(), p.Result())
			}
			return ctx
		}).
		Assess("helm install converges with nothing supplied but the two feature flags",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				args := append(chartImageArgs(),
					"--set cnpg.enabled=true",
					"--set externalSecrets.enabled=true",
					// One replica each: this feature is about the dependencies,
					// not about scheduling three Postgres pods on one kind node.
					"--set replicas=1",
					"--set cnpg.instances=1",
					"--set simulator.enabled=false",
				)
				cmd := fmt.Sprintf("helm install %s %s --kubeconfig %s --namespace %s %s --wait --timeout %s",
					release, chartPath, cfg.KubeconfigFile(), ns, strings.Join(args, " "), depsInstallTimeout)
				if p := utils.RunCommand(cmd); p.Err() != nil {
					t.Fatalf("helm install with cnpg+externalSecrets failed: %v\n%s\n%s",
						p.Err(), p.Result(), describeNS(cfg, ns))
				}
				return ctx
			}).
		Assess("CloudNativePG reports the Cluster healthy",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The operator's own opinion, not ours: a Cluster whose pods
				// are Running but whose primary has not been elected is not a
				// database anyone can use.
				out := kubectlOut(cfg, fmt.Sprintf(
					"get cluster %s -n %s -o jsonpath={.status.phase}", clusterName, ns))
				if !strings.Contains(out, "healthy") {
					t.Fatalf("CNPG Cluster phase is %q, want a healthy phase\n%s", out, describeNS(cfg, ns))
				}
				t.Logf("CNPG cluster phase: %s", out)
				return ctx
			}).
		Assess("the generated encryption key is exactly the 32 bytes Shepherd requires",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// THE assertion this file exists for. The chart asks a password
				// generator for 32 characters and b64encs them in the
				// ExternalSecret template, because Shepherd rejects anything
				// that is not base64 of exactly 32 bytes. Every link in that
				// chain is somebody else's code, and a rendering test can only
				// prove we wrote "b64enc" -- not that what comes out the far
				// end is loadable.
				var sec corev1.Secret
				if err := cfg.Client().Resources(ns).Get(ctx, secretName, ns, &sec); err != nil {
					t.Fatalf("the operator never produced %s/%s: %v\n%s", ns, secretName, err, describeNS(cfg, ns))
				}
				raw, ok := sec.Data["SHEPHERD_SECURITY_ENCRYPTION_KEY"]
				if !ok {
					t.Fatalf("generated secret has no SHEPHERD_SECURITY_ENCRYPTION_KEY, only %v", keysOf(sec.Data))
				}
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
				if err != nil {
					t.Fatalf("generated encryption key is not valid base64: %v", err)
				}
				if len(decoded) != 32 {
					t.Fatalf("generated encryption key decodes to %d bytes, want exactly 32 — "+
						"the generator length and the b64enc in the ExternalSecret template disagree", len(decoded))
				}

				if pw := sec.Data["SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD"]; len(pw) == 0 {
					t.Fatal("no admin password was generated, so nobody can sign in to this install")
				}
				return ctx
			}).
		Assess("migrations ran against the provisioned database, not some other one",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Asked of the CNPG database itself. `helm install` exiting 0
				// means the hook Job succeeded; it does not mean the Job talked
				// to the database the Deployment is about to use.
				// -U postgres, not -U shepherd: this is the unix socket inside
				// the CNPG container, which authenticates by peer. The
				// container's OS user is postgres, so asking as the
				// application role fails auth on a database that is working
				// perfectly. The DATABASE is still shepherd's -- that is what
				// the assertion is about.
				out := kubectlOut(cfg, fmt.Sprintf(
					"exec -n %s %s-1 -c postgres -- psql -U postgres -d %s -tAc %s",
					ns, clusterName, "shepherd", strconv.Quote("select count(*) from schema_migrations")))
				n, err := strconv.Atoi(strings.TrimSpace(out))
				if err != nil || n < 1 {
					t.Fatalf("expected applied migrations in the provisioned database, psql said %q (%v)\n%s",
						out, err, describeNS(cfg, ns))
				}
				t.Logf("schema_migrations rows in the CNPG database: %d", n)
				return ctx
			}).
		Assess("shepherd runs and answers through its Service",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				waitDeploymentAvailable(t, cfg, ns, release)
				ok, out := dialUntilIn(ctx, cfg, ns, "deps-health", release, 8080, true, connectDeadline)
				if !ok {
					t.Fatalf("shepherd Service never accepted a connection in %s: %s", connectDeadline, out)
				}
				return ctx
			}).
		Assess("the generated administrator password actually signs in",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The end of the chain, and the only assertion that exercises
				// all of it at once: generator -> ExternalSecret -> Secret ->
				// env -> first-boot bootstrap -> argon2 hash -> a real session.
				// Everything upstream can be right and this still fail, because
				// the password Shepherd hashed at boot is not necessarily the
				// one sitting in the Secret now.
				var sec corev1.Secret
				if err := cfg.Client().Resources(ns).Get(ctx, secretName, ns, &sec); err != nil {
					t.Fatalf("reading generated secret: %v", err)
				}
				password := strings.TrimSpace(string(sec.Data["SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD"]))

				if !signsIn(ctx, t, cfg, ns, "deps-login", release, password) {
					t.Fatalf("the generated administrator password did not sign in\n%s", describeNS(cfg, ns))
				}

				// And the negative control: a 200 above would mean nothing if
				// this endpoint answered 200 to anything at all.
				if signsIn(ctx, t, cfg, ns, "deps-login-neg", release, "definitely-not-the-generated-one") {
					t.Fatal("a wrong password also signed in, so the successful sign-in above proves nothing")
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if keepCluster() {
				return ctx
			}
			// The CNPG Cluster is a Helm hook, so it is not part of the release
			// and `helm uninstall` would leave it. Deleting the namespace takes
			// it -- which is also the documented way to remove it for real.
			_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait=false",
				cfg.KubeconfigFile(), ns))
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// kubectlOut runs a kubectl subcommand and returns its combined output.
func kubectlOut(cfg *envconf.Config, args string) string {
	p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s %s", cfg.KubeconfigFile(), args))
	return p.Result()
}

// signsIn POSTs a sign-in as a one-shot Pod and reports whether it worked.
//
// Built as a Pod object with an argv slice rather than a `kubectl run` string,
// because utils.RunCommand parses the command itself: a JSON body and a header
// value both carry characters its parser reshapes, and the first attempt
// reached curl as an unterminated quoted string. An argv slice is never
// re-parsed by anything.
//
// The answer is the exit status, not the output: `curl --fail` exits non-zero
// on any 4xx/5xx, so the Pod reaching Succeeded IS the assertion and no log
// plumbing is needed to read it.
func signsIn(ctx context.Context, t *testing.T, cfg *envconf.Config, ns, name, release, password string) bool {
	t.Helper()
	body := fmt.Sprintf(`{"username":"admin","password":%s}`, strconv.Quote(password))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:  "curl",
				Image: "curlimages/curl:8.11.1",
				Command: []string{
					"curl", "-sS", "--fail", "-o", "/dev/null",
					"-X", "POST",
					fmt.Sprintf("http://%s.%s.svc.cluster.local:8080/api/auth/local/login", release, ns),
					"-H", "Content-Type: application/json",
					// Shepherd requires this header on state-changing requests;
					// without it the POST is refused as CSRF and the test would
					// report "the password does not work" for the wrong reason.
					"-H", "X-Requested-With: XMLHttpRequest",
					"--data-binary", body,
				},
			}},
		},
	}
	_ = cfg.Client().Resources().Delete(ctx, pod)
	if err := cfg.Client().Resources().Create(ctx, pod); err != nil {
		t.Fatalf("creating sign-in probe %s: %v", name, err)
	}

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var got corev1.Pod
		if err := cfg.Client().Resources(ns).Get(ctx, name, ns, &got); err == nil {
			switch got.Status.Phase {
			case corev1.PodSucceeded:
				return true
			case corev1.PodFailed:
				return false
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("sign-in probe %s never finished within 2m", name)
	return false
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
