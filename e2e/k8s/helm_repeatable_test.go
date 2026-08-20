//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// TestHelmChartIsRepeatable covers the install paths a single fresh install
// does not: doing it twice, upgrading, and running two releases side by side.
//
// It exists because of a risk the hook-ordering fix introduced. Annotating the
// ServiceAccount, ConfigMap and Secret as pre-install/pre-upgrade hooks takes
// them OUT of Helm's release bookkeeping — hook resources are not release
// resources. Two consequences worth proving rather than assuming:
//
//   - `helm uninstall` does not remove them, so a reinstall meets objects that
//     already exist, and Helm hooks have no server-side-apply semantics.
//   - On upgrade the same hooks run again against those same objects.
//
// Omitting hook-delete-policy was right for upgrade — the Deployment references
// all three and a before-hook-creation policy would delete them mid-release —
// but "right for upgrade" and "safe to reinstall" are different claims, and
// only one of them had evidence.
func TestHelmChartIsRepeatable(t *testing.T) {
	var f *fixture
	const (
		release = "shepherd-rpt"
		second  = "shepherd-two"
	)

	feat := features.New("chart installs repeatably and upgrades").
		WithLabel("suite", "chart").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f = newFixture(ctx, t, cfg, "repeat")
			return ctx
		}).
		Assess("install #1 succeeds",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				helmRun(t, cfg, f, "install", release, simulatorOn()...)
				return ctx
			}).
		Assess("helm upgrade of the running release succeeds",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The pre-upgrade hooks fire here against objects that already
				// exist. If the missing hook-delete-policy were wrong, this is
				// where it shows.
				helmRun(t, cfg, f, "upgrade", release, simulatorOn()...)
				return ctx
			}).
		Assess("migrations remain intact after the upgrade",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// An upgrade re-runs the migrate hook. Re-running migrations
				// must be a no-op, not an error and not a duplicate.
				n, raw := f.migrationCount(t, cfg)
				if n < 1 {
					t.Fatalf("schema_migrations unreadable after upgrade: %q\n%s",
						raw, describeNS(cfg, f.ns))
				}
				return ctx
			}).
		Assess("uninstall then install #2 succeeds",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				if p := utils.RunCommand(fmt.Sprintf(
					"helm uninstall %s --kubeconfig %s --namespace %s --wait",
					release, cfg.KubeconfigFile(), f.ns,
				)); p.Err() != nil {
					t.Fatalf("helm uninstall failed: %v\n%s", p.Err(), p.Result())
				}
				// The hook-annotated ServiceAccount/ConfigMap/Secret survive
				// uninstall by design. Installing over them must still work.
				helmRun(t, cfg, f, "install", release, simulatorOn()...)
				return ctx
			}).
		Assess("a second release installs alongside the first",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// Two releases in one namespace is a real pattern (blue/green,
				// or a per-team install) and the sharpest test of whether
				// anything in the chart is named non-uniquely.
				helmRun(t, cfg, f, "install", second, simulatorOn()...)
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			f.cleanup(cfg)
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}
