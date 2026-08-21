//go:build e2ek8s

package k8s_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/utils"
)

// Shared fixtures for the Kubernetes suite.
//
// The design goal is that every feature is INDEPENDENTLY RUNNABLE and
// REPEATABLE: `go test -run TestX` must work on its own, running the whole file
// must work, and running it twice must work. That is the base every later
// Kubernetes test builds on, so the isolation rules are worth stating.
//
//	Cluster-wide, created once in TestMain:
//	  - the kind cluster, Calico, CoreDNS
//	  - the container images (kind load is a node-level operation)
//	  - ONE Postgres, in its own namespace
//
//	Per feature, created in Setup and destroyed in Teardown:
//	  - its own namespace, so releases, Services and NetworkPolicies from one
//	    feature can never be seen by another
//	  - its own DATABASE inside the shared Postgres, so migrations from one
//	    feature cannot satisfy or corrupt another's
//
// The first version of this shared one namespace between features, and the
// second feature to run failed instantly because the Postgres Pod already
// existed. Sharing a namespace also makes ordering matter, which is the thing
// that turns a suite into a house of cards.

const (
	// infraNamespace holds the one Postgres every feature borrows. Fixed rather
	// than random so a kept cluster is easy to inspect.
	infraNamespace = "shepherd-e2e-infra"
	pgName         = "postgres"
	pgSuperUser    = "shepherd"
	pgPassword     = "e2e"
	// pgBootstrapDB is the database created by the image's own env; per-feature
	// databases are created inside this server, not this database.
	pgBootstrapDB = "postgres"

	chartPath       = "../../deploy/helm/shepherd"
	chartSecretName = "shepherd-e2e-secrets"

	// encryptionKey decodes to exactly "e2e-test-key-not-a-real-secret!!" — 32
	// bytes, which the config validator requires. An obviously-fake fixture that
	// must never look like something to copy into a deployment. (A first attempt
	// decoded to 29 bytes and the migrate Job died with "must be a
	// base64-encoded 32-byte value".)
	encryptionKey = "ZTJlLXRlc3Qta2V5LW5vdC1hLXJlYWwtc2VjcmV0ISE="
)

// fixture is one feature's isolated slice of the cluster.
type fixture struct {
	ns string // this feature's namespace
	db string // this feature's database inside the shared Postgres
}

// newFixture creates a namespace and a database for one feature, plus the
// Secret the chart needs. Safe to call for a name that was used by a previous
// run: it deletes first, so re-running a feature is never blocked by its own
// leftovers.
func newFixture(ctx context.Context, t *testing.T, cfg *envconf.Config, name string) *fixture {
	t.Helper()
	f := &fixture{ns: "shepherd-" + name, db: strings.ReplaceAll(name, "-", "_")}

	// Idempotence first: a killed run leaves a namespace behind, and the next
	// run must not inherit its state or fail on AlreadyExists.
	_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait --timeout=2m",
		cfg.KubeconfigFile(), f.ns))
	if p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s create namespace %s",
		cfg.KubeconfigFile(), f.ns)); p.Err() != nil {
		t.Fatalf("creating namespace %s: %v: %s", f.ns, p.Err(), p.Result())
	}

	// A per-feature database, so one feature's migrations cannot make another's
	// assertions pass. DROP first for the same reason the namespace is deleted.
	execPSQL(t, cfg, pgBootstrapDB, fmt.Sprintf("DROP DATABASE IF EXISTS %s", f.db))
	execPSQL(t, cfg, pgBootstrapDB, fmt.Sprintf("CREATE DATABASE %s", f.db))

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: chartSecretName, Namespace: f.ns},
		StringData: map[string]string{
			"SHEPHERD_DATABASE_URL": fmt.Sprintf(
				"postgres://%s:%s@%s.%s.svc.cluster.local:5432/%s?sslmode=disable",
				pgSuperUser, pgPassword, pgName, infraNamespace, f.db),
			"SHEPHERD_SECURITY_ENCRYPTION_KEY": encryptionKey,
		},
	}
	if err := cfg.Client().Resources().Create(ctx, sec); err != nil {
		t.Fatalf("creating chart secret in %s: %v", f.ns, err)
	}
	return f
}

// cleanup removes the feature's namespace. Releases inside it go with it, so
// features do not each need their own helm uninstall bookkeeping.
func (f *fixture) cleanup(cfg *envconf.Config) {
	if keepCluster() {
		return
	}
	_ = utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s delete namespace %s --ignore-not-found --wait=false",
		cfg.KubeconfigFile(), f.ns))
}

// query runs SQL against this feature's own database and returns stdout.
func (f *fixture) query(t *testing.T, cfg *envconf.Config, sql string) string {
	t.Helper()
	return psqlIn(cfg, f.db, sql)
}

// migrationCount is the assertion that matters after an install: helm exiting 0
// says the hook ran, not that the schema moved.
func (f *fixture) migrationCount(t *testing.T, cfg *envconf.Config) (int, string) {
	t.Helper()
	out := f.query(t, cfg, "select count(*) from schema_migrations")
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, out
	}
	return n, out
}

// --- shared Postgres -------------------------------------------------------

// deploySharedPostgres runs once per cluster, in its own namespace. Deliberately
// a bare Pod and Service rather than a chart dependency: it is a test fixture,
// not a recommendation, and must not drift into looking like one.
func deploySharedPostgres(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	if p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s create namespace %s",
		cfg.KubeconfigFile(), infraNamespace)); p.Err() != nil && !strings.Contains(p.Result(), "AlreadyExists") {
		return ctx, fmt.Errorf("creating infra namespace: %w: %s", p.Err(), p.Result())
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: pgName, Namespace: infraNamespace, Labels: map[string]string{"app": pgName},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "postgres",
				Image: "postgres:16-alpine",
				Env: []corev1.EnvVar{
					{Name: "POSTGRES_PASSWORD", Value: pgPassword},
					{Name: "POSTGRES_USER", Value: pgSuperUser},
					{Name: "POSTGRES_DB", Value: pgBootstrapDB},
				},
				Ports: []corev1.ContainerPort{{ContainerPort: 5432}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						Exec: &corev1.ExecAction{Command: []string{"pg_isready", "-U", pgSuperUser}},
					},
					InitialDelaySeconds: 3,
					PeriodSeconds:       3,
				},
			}},
		},
	}
	if err := cfg.Client().Resources().Create(ctx, pod); err != nil {
		return ctx, fmt.Errorf("creating postgres pod: %w", err)
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: pgName, Namespace: infraNamespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": pgName},
			Ports: []corev1.ServicePort{{
				Port: 5432, TargetPort: intstr.FromInt32(5432), Protocol: corev1.ProtocolTCP,
			}},
		},
	}
	if err := cfg.Client().Resources().Create(ctx, svc); err != nil {
		return ctx, fmt.Errorf("creating postgres service: %w", err)
	}
	if err := wait.For(
		conditions.New(cfg.Client().Resources()).PodReady(pod),
		wait.WithTimeout(3*time.Minute),
		wait.WithInterval(3*time.Second),
	); err != nil {
		return ctx, fmt.Errorf("postgres never became ready: %w", err)
	}
	return ctx, nil
}

// loadImages puts the locally-built images into the kind nodes, once per
// cluster. The chart is installed with pullPolicy=Never so a missing image
// fails loudly here rather than the cluster silently pulling something else.
//
// It also warns when an image looks older than the source it should contain —
// see warnIfImageLooksStale for why that is a warning and not a failure.
func loadImages(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
	newest, newestPath, err := newestSourceMTime()
	if err != nil {
		return ctx, fmt.Errorf("scanning source tree: %w", err)
	}
	for _, img := range []string{"shepherd:local", "shepherd-simulator:local"} {
		if p := utils.RunCommand(fmt.Sprintf("docker image inspect %s", img)); p.Err() != nil {
			return ctx, fmt.Errorf(
				"image %s is not present locally — build it first with "+
					"`make docker-build-local docker-build-simulator`: %w", img, p.Err())
		}
		if err := warnIfImageLooksStale(img, newest, newestPath); err != nil {
			return ctx, err
		}
		if p := utils.RunCommand(fmt.Sprintf("kind load docker-image %s --name %s", img, clusterName)); p.Err() != nil {
			return ctx, fmt.Errorf("loading %s into kind: %w: %s", img, p.Err(), p.Result())
		}
	}
	return ctx, nil
}

// warnIfImageLooksStale warns when an image predates the newest source file.
//
// A WARNING, not a failure, and the distinction is the point. The real guard is
// the `make e2e-k8s` prerequisite on docker-build-local/docker-build-simulator,
// which is the pattern every other e2e target already uses — and it is
// CORRECT, because Docker's build cache is content-addressed: a real source
// change misses the cache and rebuilds, an unchanged tree reuses a valid image.
//
// This check cannot see any of that. It compares mtimes, so `touch`ing a file
// with no content change makes it fire forever while the image is perfectly
// up to date — observed exactly that while building it. Keeping it as a hard
// failure would mean a suite that refuses to run for a reason that is not true.
// It stays as a hint for anyone invoking `go test -tags e2ek8s` directly and
// skipping the Makefile.
func warnIfImageLooksStale(img string, newest time.Time, newestPath string) error {
	if os.Getenv("E2E_K8S_ALLOW_STALE_IMAGES") == "1" {
		return nil
	}
	p := utils.RunCommand(fmt.Sprintf("docker image inspect %s --format {{.Created}}", img))
	if p.Err() != nil {
		return fmt.Errorf("reading created time of %s: %w", img, p.Err())
	}
	built, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(p.Result()))
	if err != nil {
		// Not worth failing the run over an unparseable timestamp; the
		// prerequisite in `make e2e-k8s` is the primary guard.
		log.Printf("could not parse created time for %s (%q) — skipping staleness check", img, p.Result())
		return nil
	}
	if built.Before(newest) {
		log.Printf("WARNING: image %s was built %s but %s is newer (%s). "+
			"If you did not just run `make e2e-k8s` (which rebuilds), the cluster may be testing "+
			"stale code — run `make docker-build-local docker-build-simulator`. "+
			"A cache-hit rebuild leaves the image timestamp unchanged, so this can be a false alarm.",
			img, built.Format(time.RFC3339), newestPath, newest.Format(time.RFC3339))
	}
	return nil
}

// newestSourceMTime finds the most recently modified file that ends up inside
// an image: Go sources and the built SPA. Test files are excluded — editing a
// spec must not force an image rebuild, since specs run outside the cluster.
func newestSourceMTime() (time.Time, string, error) {
	var newest time.Time
	var newestPath string
	roots := []string{"../../cmd", "../../internal", "../../gen"}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil //nolint:nilerr // a missing root is not fatal here
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if info.ModTime().After(newest) {
				newest, newestPath = info.ModTime(), path
			}
			return nil
		})
		if err != nil {
			return newest, newestPath, err
		}
	}
	return newest, newestPath, nil
}

// --- psql helpers ----------------------------------------------------------

func psqlIn(cfg *envconf.Config, db, sql string) string {
	p := utils.RunCommand(fmt.Sprintf(
		"kubectl --kubeconfig %s -n %s exec %s -- psql -U %s -d %s -tAc %s",
		cfg.KubeconfigFile(), infraNamespace, pgName, pgSuperUser, db, strconv.Quote(sql),
	))
	return p.Result()
}

func execPSQL(t *testing.T, cfg *envconf.Config, db, sql string) {
	t.Helper()
	if out := psqlIn(cfg, db, sql); strings.Contains(out, "ERROR") {
		t.Fatalf("psql %q failed: %s", sql, out)
	}
}

// --- helm helpers ----------------------------------------------------------

// helmRun runs install or upgrade with the fixture's values and fails with the
// cluster's own view of the namespace when it does not work.
func helmRun(t *testing.T, cfg *envconf.Config, f *fixture, verb, release string, extra ...string) {
	t.Helper()
	args := []string{
		// registry is cleared because the chart defaults to the released
		// ghcr.io images while this suite loads bare local tags into kind.
		"--set image.registry=", "--set image.repository=shepherd", "--set image.tag=local", "--set image.pullPolicy=Never",
		// The simulator deploys by default since v0.0.1, so every install —
		// including the pure-defaults one — needs its local image wired.
		"--set simulator.image.registry=", "--set simulator.image.repository=shepherd-simulator",
		"--set simulator.image.tag=local", "--set simulator.image.pullPolicy=Never",
		"--set existingSecret=" + chartSecretName,
	}
	args = append(args, extra...)
	p := utils.RunCommand(fmt.Sprintf(
		"helm %s %s %s --kubeconfig %s --namespace %s %s --wait --timeout 5m",
		verb, release, chartPath, cfg.KubeconfigFile(), f.ns, strings.Join(args, " "),
	))
	if p.Err() != nil {
		t.Fatalf("helm %s %s failed: %v\n%s\n%s", verb, release, p.Err(), p.Result(), describeNS(cfg, f.ns))
	}
}

// simulatorOn is the extra flags a feature adds when it depends on the
// sandbox. Since v0.0.1 the chart enables the simulator by default (its local
// image overrides live in helmRun's base args), so this pins the flag
// explicitly — a feature that NEEDS the sandbox must not silently lose it if
// the default ever flips back — and drops replicas to one for speed.
func simulatorOn() []string {
	return []string{
		"--set simulator.enabled=true",
		"--set replicas=1",
	}
}

// describeNS gathers enough to diagnose a failure after the cluster is gone. A
// pod list alone is not enough: a Job that never creates a Pod shows as 0/1 with
// nothing to look at and the reason lives in its events, while a Pod that starts
// and exits needs its logs.
func describeNS(cfg *envconf.Config, ns string) string {
	var out string
	for _, cmd := range []string{
		"get pods,jobs,deploy,svc,secrets,networkpolicy -o wide",
		"get events --sort-by=.lastTimestamp",
		"logs --selector=app.kubernetes.io/name=shepherd --tail=40 --prefix",
	} {
		p := utils.RunCommand(fmt.Sprintf("kubectl --kubeconfig %s -n %s %s", cfg.KubeconfigFile(), ns, cmd))
		out += fmt.Sprintf("\n--- kubectl -n %s %s ---\n%s\n", ns, cmd, p.Result())
	}
	return out
}
