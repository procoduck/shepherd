//go:build e2ek8s

package k8s_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/pkg/utils"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

const (
	// publishedChartRef is where release.yml publishes the chart.
	publishedChartRef = "oci://ghcr.io/procoduck/charts/shepherd"
	// publishedChartRepo is the same repository as the OCI registry API
	// names it.
	publishedChartRepo = "procoduck/charts/shepherd"
	// previousChartEnv overrides the derived previous version.
	previousChartEnv = "E2E_K8S_PREVIOUS_CHART"
)

// TestHelmChartUpgradesFromPreviousRelease is the upgrade an operator
// actually runs: the previous PUBLISHED chart, with the images it was
// released with, carrying real state, then `helm upgrade` to the chart and
// images built from this tree (#425).
//
// TestHelmChartIsRepeatable upgrades a release to the SAME chart, which
// proves the hooks re-run cleanly but cannot see what changes between
// versions: a migration that fails on a populated database, a renamed value
// the old release's stored values no longer satisfy, a pod that will not
// start against the schema the new hook left, an agent token or served
// config the new binary reads differently. Those only show on a real
// previous version with data in it.
//
// The previous version is derived, not pinned: the highest non-prerelease
// chart in ghcr.io that sorts below Chart.yaml's version (an -rc.N tree
// upgrades from the last release, never from its own earlier rc).
// E2E_K8S_PREVIOUS_CHART overrides it. The previous chart and its image are
// pulled from ghcr.io, so this feature needs network — CI's runners have it.
// Offline, outside CI, it skips with the reason; in CI an unreachable
// registry is a failure, so the feature cannot silently stop running.
func TestHelmChartUpgradesFromPreviousRelease(t *testing.T) {
	var (
		f           *fixture
		api         *shepherdAPI
		previous    string
		current     string
		latest      int
		orgID       string
		pipelineID  string
		tokenID     string
		tokenSecret string
		prevMigrate int
	)
	const (
		release     = "shepherd-upg"
		password    = "e2e-upgrade-admin-pass"
		cluster     = "upgrade-cluster"
		instanceID  = "upgrade-collector-1"
		pipeName    = "upgrade-seed"
		pipeBlock   = `declare "pipe_upgrade_seed"`
		pipeContent = `prometheus.exporter.self "upgrade" { }`
	)
	seedMatchers := []string{`cluster="` + cluster + `"`, `role="metrics"`}

	feat := features.New("chart upgrades from the previous published release with state intact").
		WithLabel("suite", "chart").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			var err error
			if current, err = treeChartVersion(chartPath); err != nil {
				t.Fatal(err)
			}
			if latest, err = latestTreeMigration("../../internal/migrations/sql"); err != nil {
				t.Fatal(err)
			}
			previous = resolvePreviousChart(ctx, t, current)
			t.Logf("upgrading %s %s → tree chart %s (latest migration %04d)", publishedChartRef, previous, current, latest)

			f = newFixture(ctx, t, cfg, "upgrade")
			out := kubectlArgv(cfg, "-n", f.ns, "patch", "secret", chartSecretName, "--type=merge",
				"-p", fmt.Sprintf(`{"stringData":{"SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD":%q}}`, password))
			if !strings.Contains(out, "patched") {
				t.Fatalf("setting the bootstrap admin password: %s", out)
			}
			return ctx
		}).
		Assess("the previous published chart installs with its released images",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// No chartImageArgs: the previous release runs exactly what it
				// was published with (ghcr.io/procoduck/shepherd:<its
				// appVersion>), pulled by the kind nodes. The simulator is off
				// so only the one image has to come over the network.
				p := utils.RunCommand(fmt.Sprintf(
					"helm install %s %s --version %s --kubeconfig %s --namespace %s "+
						"--set existingSecret=%s --set replicas=1 --set simulator.enabled=false --wait --timeout 10m",
					release, publishedChartRef, previous, cfg.KubeconfigFile(), f.ns, chartSecretName))
				if p.Err() != nil {
					t.Fatalf("helm install of the published chart %s failed: %v\n%s\n%s",
						previous, p.Err(), p.Result(), describeNS(cfg, f.ns))
				}
				if got := releaseChart(t, cfg, f.ns, release); got != "shepherd-"+previous {
					t.Fatalf("installed release reports chart %q, want shepherd-%s", got, previous)
				}
				var dirty bool
				prevMigrate, dirty = f.migrationVersion(t, cfg)
				if dirty || prevMigrate < 1 || prevMigrate > latest {
					t.Fatalf("the previous release left schema_migrations at %d (dirty=%t); the tree's latest is %d",
						prevMigrate, dirty, latest)
				}
				t.Logf("previous release migrated to %04d", prevMigrate)
				return ctx
			}).
		Assess("state is seeded through the previous release: org, agent token, collector, enabled pipeline",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				api = newShepherdAPI(t, cfg, f.ns, release)
				api.login(t, "admin", password)
				org, err := api.admin.CreateOrg(ctx, connect.NewRequest(&mgmtv1.CreateOrgRequest{
					Name: "upgrade", DisplayName: "Upgrade", AdminGroupId: "upgrade-admins",
				}))
				if err != nil {
					t.Fatalf("CreateOrg: %v", err)
				}
				orgID = org.Msg.GetId()
				token, err := api.admin.CreateAgentToken(ctx, connect.NewRequest(&mgmtv1.CreateAgentTokenRequest{
					Name: "upgrade", OrgId: orgID,
				}))
				if err != nil {
					t.Fatalf("CreateAgentToken: %v", err)
				}
				tokenID, tokenSecret = token.Msg.GetId(), token.Msg.GetSecret()

				agent := newAgentClient(api.base, tokenID, tokenSecret)
				if _, err := agent.RegisterCollector(ctx, connect.NewRequest(&collectorv1.RegisterCollectorRequest{
					Id: instanceID, Name: instanceID, LocalAttributes: map[string]string{"cluster": cluster, "role": "metrics"},
				})); err != nil {
					t.Fatalf("RegisterCollector with the new token: %v", err)
				}
				if _, err := api.admin.ClaimCluster(ctx, connect.NewRequest(&mgmtv1.ClaimClusterRequest{
					Cluster: cluster, OrgId: orgID,
				})); err != nil {
					t.Fatalf("ClaimCluster: %v", err)
				}
				p, err := api.pipelines.CreatePipeline(ctx, connect.NewRequest(&mgmtv1.CreatePipelineRequest{
					OrgId: orgID, Name: pipeName, Contents: pipeContent, Matchers: seedMatchers,
				}))
				if err != nil {
					t.Fatalf("CreatePipeline: %v", err)
				}
				pipelineID = p.Msg.GetId()
				if _, err := api.pipelines.EnablePipeline(ctx, connect.NewRequest(&mgmtv1.EnablePipelineRequest{
					OrgId: orgID, Id: pipelineID,
				})); err != nil {
					t.Fatalf("EnablePipeline: %v", err)
				}
				// The baseline before the upgrade: the previous release serves
				// the collector its pipeline. Without this, a missing block
				// after the upgrade could be a seeding mistake.
				waitServedPipeline(ctx, t, cfg, f.ns, release, agent, cluster, instanceID, pipeBlock)
				api.close()
				api = nil
				return ctx
			}).
		Assess("helm upgrade to the tree's chart and images succeeds",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The same flags as the install plus the local images: an
				// operator's `helm upgrade` with their values, the chart from
				// this tree.
				helmRun(t, cfg, f, "upgrade", release, "--set replicas=1", "--set simulator.enabled=false")
				if got := releaseChart(t, cfg, f.ns, release); got != "shepherd-"+current {
					t.Fatalf("upgraded release reports chart %q, want shepherd-%s", got, current)
				}
				waitDeploymentAvailable(t, cfg, f.ns, release)
				img := kubectlArgv(cfg, "-n", f.ns, "get", "deploy", release,
					"-o", "jsonpath={.spec.template.spec.containers[0].image}")
				if strings.TrimSpace(img) != "shepherd:local" {
					t.Fatalf("after the upgrade the Deployment runs %q, not the tree's shepherd:local", img)
				}
				return ctx
			}).
		Assess("the migrate hook took the schema to the tree's latest migration",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				v, dirty := f.migrationVersion(t, cfg)
				if dirty || v != latest {
					t.Fatalf("schema_migrations is at %d (dirty=%t) after the upgrade; the tree's latest migration is %d (was %d before)\n%s",
						v, dirty, latest, prevMigrate, describeNS(cfg, f.ns))
				}
				t.Logf("schema %04d → %04d", prevMigrate, v)
				return ctx
			}).
		Assess("the upgraded server is ready",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// /readyz pings the database and compares schema_migrations
				// with the migrations embedded in the binary: ready means the
				// new binary accepts the schema the hook left.
				api = newShepherdAPI(t, cfg, f.ns, release)
				status, body := getReadyz(ctx, t, api.base)
				if status != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
					t.Fatalf("/readyz after the upgrade: HTTP %d %s\n%s", status, body, describeNS(cfg, f.ns))
				}
				return ctx
			}).
		Assess("the seeded org, pipeline and the admin's password survive",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				api.login(t, "admin", password)
				orgs, err := api.admin.ListOrgs(ctx, connect.NewRequest(&mgmtv1.ListOrgsRequest{}))
				if err != nil {
					t.Fatalf("ListOrgs: %v", err)
				}
				found := false
				for _, o := range orgs.Msg.GetItems() {
					found = found || o.GetId() == orgID
				}
				if !found {
					t.Fatalf("the org seeded before the upgrade (%s) is gone: %v", orgID, orgs.Msg.GetItems())
				}
				p, err := api.pipelines.GetPipeline(ctx, connect.NewRequest(&mgmtv1.GetPipelineRequest{OrgId: orgID, Id: pipelineID}))
				if err != nil {
					t.Fatalf("GetPipeline after the upgrade: %v", err)
				}
				if !p.Msg.GetEnabled() || p.Msg.GetContents() != pipeContent || p.Msg.GetSource() != "ui" ||
					strings.Join(p.Msg.GetMatchers(), ",") != strings.Join(seedMatchers, ",") {
					t.Fatalf("the seeded pipeline changed across the upgrade: enabled=%t source=%q matchers=%v contents=%q",
						p.Msg.GetEnabled(), p.Msg.GetSource(), p.Msg.GetMatchers(), p.Msg.GetContents())
				}
				return ctx
			}).
		Assess("the collector, on its pre-upgrade token, is still served its pipeline",
			func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
				// The same instance id and the token minted by the previous
				// release: the new binary must still authenticate it and
				// assemble the stored pipeline into its served config.
				agent := newAgentClient(api.base, tokenID, tokenSecret)
				waitServedPipeline(ctx, t, cfg, f.ns, release, agent, cluster, instanceID, pipeBlock)
				return ctx
			}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if api != nil {
				api.close()
			}
			if f != nil {
				f.cleanup(cfg)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, feat)
}

// resolvePreviousChart returns the chart version to upgrade from:
// E2E_K8S_PREVIOUS_CHART when set, else the highest published release below
// current. An unreachable registry skips outside CI (an offline workstation
// cannot run this feature at all) and fails in CI (where a skip would let the
// feature quietly stop running).
func resolvePreviousChart(ctx context.Context, t *testing.T, current string) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv(previousChartEnv)); v != "" {
		if _, err := parseChartSemver(v); err != nil {
			t.Fatalf("%s: %v", previousChartEnv, err)
		}
		return v
	}
	tags, err := publishedChartTags(ctx)
	if err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("listing the published charts to upgrade from: %v", err)
		}
		t.Skipf("SKIP previous-release upgrade: cannot list %s (offline?): %v — the feature pulls the "+
			"previous chart and image from ghcr.io; set %s=<version> once the network is back", publishedChartRef, err, previousChartEnv)
	}
	v, err := previousStableChart(tags, current)
	if err != nil {
		t.Fatalf("choosing the chart to upgrade from: %v", err)
	}
	return v
}

// migrationVersion is golang-migrate's one schema_migrations row.
func (f *fixture) migrationVersion(t *testing.T, cfg *envconf.Config) (int, bool) {
	t.Helper()
	// psql -tA separates columns with "|": "50|f".
	out := strings.TrimSpace(f.query(t, cfg, "select version, dirty from schema_migrations"))
	ver, dirty, ok := strings.Cut(out, "|")
	n, err := strconv.Atoi(ver)
	if !ok || err != nil {
		t.Fatalf("reading schema_migrations: psql said %q\n%s", out, describeNS(cfg, f.ns))
	}
	return n, dirty == "t" || dirty == "true"
}

// releaseChart is the chart (name-version) helm records for the release.
func releaseChart(t *testing.T, cfg *envconf.Config, ns, release string) string {
	t.Helper()
	p := utils.RunCommand(fmt.Sprintf("helm list --kubeconfig %s --namespace %s --filter ^%s$ -o json",
		cfg.KubeconfigFile(), ns, release))
	var rels []struct {
		Name  string `json:"name"`
		Chart string `json:"chart"`
	}
	if p.Err() != nil || json.Unmarshal([]byte(p.Result()), &rels) != nil || len(rels) != 1 {
		t.Fatalf("helm list for %s: %v\n%s", release, p.Err(), p.Result())
	}
	return rels[0].Chart
}

// newAgentClient is a collector's own view of Shepherd: the collector.v1
// service Alloy's remotecfg polls, with the agent token as basic auth.
func newAgentClient(base, tokenID, tokenSecret string) collectorv1connect.CollectorServiceClient {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(tokenID+":"+tokenSecret))
	client := &http.Client{Timeout: 30 * time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", auth)
		return http.DefaultTransport.RoundTrip(req)
	})}
	return collectorv1connect.NewCollectorServiceClient(client, base)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// waitServedPipeline polls GetConfig as the collector until its served config
// carries block. EnablePipeline recomputes the serve cache in the background,
// so the first poll after it may still hold the beacon-only config.
func waitServedPipeline(ctx context.Context, t *testing.T, cfg *envconf.Config, ns, release string,
	agent collectorv1connect.CollectorServiceClient, cluster, instanceID, block string,
) {
	t.Helper()
	end := time.Now().Add(2 * time.Minute)
	var content string
	var lastErr error
	for time.Now().Before(end) {
		resp, err := agent.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: instanceID, LocalAttributes: map[string]string{"cluster": cluster, "role": "metrics"},
		}))
		lastErr = err
		if err == nil {
			content = resp.Msg.GetContent()
			if strings.Contains(content, block) {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("collector %s was not served %s within 2m (last error %v)\n--- served ---\n%s\n--- shepherd logs ---\n%s",
		instanceID, block, lastErr, content, kubectlArgv(cfg, "-n", ns, "logs", "deploy/"+release, "--tail=60"))
}

func getReadyz(ctx context.Context, t *testing.T, base string) (int, string) {
	t.Helper()
	end := time.Now().Add(time.Minute)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body) //nolint:errcheck // diagnostics only
			_ = resp.Body.Close()            //nolint:errcheck // read-only body
			if resp.StatusCode == http.StatusOK || time.Now().After(end) {
				return resp.StatusCode, string(body)
			}
		} else if time.Now().After(end) {
			t.Fatalf("/readyz: %v", err)
		}
		time.Sleep(3 * time.Second)
	}
}

// publishedChartTags lists the chart's tags in ghcr.io with an anonymous pull
// token: the registry API helm itself uses, so no helm subcommand (which has
// none for listing OCI tags) and no new dependency.
func publishedChartTags(ctx context.Context) ([]string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var tok struct {
		Token string `json:"token"`
	}
	tokURL := "https://ghcr.io/token?scope=" + url.QueryEscape("repository:"+publishedChartRepo+":pull")
	if err := getJSON(ctx, client, tokURL, "", &tok); err != nil {
		return nil, fmt.Errorf("anonymous ghcr.io token: %w", err)
	}
	var tags []string
	next := "/v2/" + publishedChartRepo + "/tags/list?n=1000"
	for page := 0; next != "" && page < 20; page++ {
		var list struct {
			Tags []string `json:"tags"`
		}
		link, err := getJSONLink(ctx, client, "https://ghcr.io"+next, tok.Token, &list)
		if err != nil {
			return nil, fmt.Errorf("listing %s tags: %w", publishedChartRef, err)
		}
		tags = append(tags, list.Tags...)
		next = link
	}
	return tags, nil
}

func getJSON(ctx context.Context, client *http.Client, u, bearer string, out any) error {
	_, err := getJSONLink(ctx, client, u, bearer, out)
	return err
}

// linkNextRE reads the registry's pagination header: Link: </v2/...>; rel="next".
var linkNextRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func getJSONLink(ctx context.Context, client *http.Client, u, bearer string, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d: %.200s", u, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return "", fmt.Errorf("GET %s: %w", u, err)
	}
	if m := linkNextRE.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
		return m[1], nil
	}
	return "", nil
}
