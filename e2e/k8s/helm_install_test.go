//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// TestHelmChartInstalls covers what `make helm-lint` structurally cannot.
//
// helm-lint runs `helm lint` and `helm template`. Neither applies anything, so
// a chart that renders perfectly and fails on apply ships undetected. Three
// such defects were live when this was written: an HTTPRoute rendered by
// default against a cluster with no Gateway API CRDs, and a pre-install hook
// whose ServiceAccount, ConfigMap and Secret were all created after it.
//
// It is also the prerequisite for the containment probes, which need a real
// simulator Pod with the chart's real NetworkPolicy attached.
func TestHelmChartInstalls(t *testing.T) {
	var f *fixture
	const release = "shepherd"

	feat := features.New("helm chart installs and runs").
		WithLabel("suite", "chart").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f = newFixture(ctx, t, cfg, "install")
			return ctx
		}).
		Assess("helm install succeeds with the simulator enabled",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				helmRun(t, cfg, f, "install", release, simulatorOn()...)
				return ctx
			}).
		Assess("migrations were actually applied to the database",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// NOT "the Job still exists": it is a hook with
				// hook-delete-policy: hook-succeeded, so Helm removes it on
				// success and listing Jobs finds nothing. Asserting on the Job
				// would fail for the one reason that means everything worked.
				//
				// Nor is "helm install succeeded" enough on its own — that says
				// the hook exited 0, not that the schema moved. Ask the database.
				n, raw := f.migrationCount(t, cfg)
				if n < 1 {
					t.Fatalf("expected at least one applied migration, psql said %q\n%s",
						raw, describeNS(cfg, f.ns))
				}
				t.Logf("schema_migrations rows: %d", n)
				return ctx
			}).
		Assess("the shepherd Deployment becomes Available",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				waitDeploymentAvailable(t, cfg, f.ns, release)
				return ctx
			}).
		Assess("the simulator Deployment becomes Available",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// If this is what fails, containment is untestable — worth its
				// own assertion rather than being folded into the one above.
				waitDeploymentAvailable(t, cfg, f.ns, release+"-simulator")
				return ctx
			}).
		Assess("shepherd answers through its Service",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Through the Service, not the Pod IP: that exercises the
				// selector, which charts get wrong and templating cannot catch.
				ok, out := dialUntilIn(ctx, cfg, f.ns, "chart-health", release, 8080, true, connectDeadline)
				if !ok {
					t.Fatalf("shepherd Service never accepted a connection in %s: %s", connectDeadline, out)
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// TestHelmChartDefaultValuesInstall installs with the chart's own defaults —
// no --set beyond what a cluster genuinely cannot supply.
//
// Every other spec passes overrides, so none exercises what a user actually
// gets from `helm install shepherd ./shepherd`. The HTTPRoute defect lived
// exactly there: every values file in the repo set route.enabled=false, so the
// default nobody tested was the one that was broken.
func TestHelmChartDefaultValuesInstall(t *testing.T) {
	var f *fixture
	const release = "shepherd-def"

	feat := features.New("chart installs with default values").
		WithLabel("suite", "chart").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f = newFixture(ctx, t, cfg, "defaults")
			return ctx
		}).
		Assess("install with defaults succeeds",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Only the image (local) and the Secret (DB URL, encryption key)
				// are overridden. Everything else stays at the chart's default.
				helmRun(t, cfg, f, "install", release)
				return ctx
			}).
		Assess("the simulator is NOT deployed by default",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// simulator.enabled defaults false and must stay false: S3
				// executes user-authored config and has open containment
				// findings. A default install that quietly shipped it would be
				// the worst kind of regression.
				p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s get deploy -o name",
					cfg.KubeconfigFile(), f.ns))
				if strings.Contains(p.Result(), release+"-simulator") {
					t.Fatalf("the simulator Deployment exists after a DEFAULT install — "+
						"simulator.enabled must default to false:\n%s", p.Result())
				}
				return ctx
			}).
		Assess("migrations ran on a default install too",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				n, raw := f.migrationCount(t, cfg)
				if n < 1 {
					t.Fatalf("expected at least one applied migration, psql said %q\n%s",
						raw, describeNS(cfg, f.ns))
				}
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

func waitDeploymentAvailable(t *testing.T, cfg *envconf.Config, ns, name string) {
	t.Helper()
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := wait.For(
		conditions.New(cfg.Client().Resources()).DeploymentAvailable(name, ns),
		wait.WithTimeout(5*time.Minute),
		wait.WithInterval(5*time.Second),
	); err != nil {
		diag := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s describe deployment %s",
			cfg.KubeconfigFile(), ns, name))
		t.Fatalf("deployment %q never became Available: %v\n%s", dep.Name, err, diag.Result())
	}
}
