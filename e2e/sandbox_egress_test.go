//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The executable egress proof (VB-1 §6.4 "Security containment", finding H4).
//
// THIS FILE IS THE REACHABILITY CONTROL. Not a backstop to one — the control.
// The two text gates in §6.4's story (the transform's keep lists, the
// simulator's CheckEndpoints) bound which authored VALUES leave a user's graph;
// neither can bound where a running config connects, because a discovery.relabel
// rule computes __address__ at runtime from label data and the address need
// never appear as a literal anywhere. The static rule that used to claim
// otherwise (simulate's P5, address_not_harness) was deleted for being
// permeable and deny-everything at once. What remains is the network, and until
// this file existed NOTHING verified its effect: internal/simsvc/
// compose_containment_test.go asserts the YAML DECLARATION; the observable
// probes lived only in a proof document and had been run by hand.
//
// Four probes, and the first one is the reason the other three mean anything:
//
//	P-control    dial the canary from the DEFAULT network. MUST SUCCEED.
//	P-deny-name  dial the canary BY NAME from the simulator's own network
//	             namespace. MUST FAIL.
//	P-deny-ip    dial the canary BY IP from the same namespace. MUST FAIL.
//	             Separates routing denial from mere absence of DNS.
//	P-topology   the simulator's attached network set is exactly
//	             {shepherd-e2e-sim} and that network reports Internal=true —
//	             read from `docker inspect`, i.e. from the running daemon, not
//	             from the YAML that asked for it.
//
// `--network container:<simulator>` is the exact namespace the sandbox runs in,
// not a proxy for it: internal/simsvc/runner.go:90 starts Alloy with
// exec.CommandContext INSIDE the simulator container, so the sandbox has no
// network namespace of its own. P-topology's container-set assertion is what
// makes that architectural fact loud if it ever changes — a sandbox moved into
// its own container would show up as a second member of shepherd-e2e-sim, and
// these probes would then be testing the wrong namespace.

// probeImage is the one image both the control probe and the denial probes
// run, so "it worked here and not there" can only be about the network.
const probeImage = "alpine:3.22"

// Two labels, not one. "sandbox-sim" keeps `make e2e` filtering this out
// alongside the rest of the S3 suite; "sandbox-egress" is what CI runs on its
// own (`make e2e-egress`), because the S3 run-lifecycle spec in
// sandbox_sim_test.go is red on a pre-existing renderer defect (finding M13)
// and a known-red CI job teaches people to ignore CI.
var _ = Describe("Scenario sandbox-sim: the sandbox cannot reach anything off its own network", Ordered, Label("sandbox-sim"), Label("sandbox-egress"), func() {
	const (
		canaryService = "egress-canary"
		canaryPort    = "8080"
		simNetwork    = "shepherd-e2e-sim"
		e2eNetwork    = "shepherd-e2e"
	)

	var (
		simulatorID string
		canaryIP    string
	)

	BeforeAll(func() {
		simulatorID = strings.TrimSpace(dockerOut("compose", "-f", "docker-compose.e2e.yaml", "--profile", "sim", "ps", "-q", "simulator"))
		Expect(simulatorID).NotTo(BeEmpty(),
			"the simulator container must be up; these probes are meaningless against a stack started without --profile sim")

		canaryID := strings.TrimSpace(dockerOut("compose", "-f", "docker-compose.e2e.yaml", "--profile", "sim", "ps", "-q", canaryService))
		Expect(canaryID).NotTo(BeEmpty(), "the egress-canary container must be up")
		canaryIP = strings.TrimSpace(dockerOut("inspect", "-f",
			fmt.Sprintf("{{ (index .NetworkSettings.Networks %q).IPAddress }}", e2eNetwork), canaryID))
		Expect(canaryIP).NotTo(BeEmpty(), "the canary must have an address on %s", e2eNetwork)
	})

	// P-control. Same image, same command, same canary — only the network
	// differs. If this fails, the two denial probes below prove nothing at all,
	// which is exactly the trap finding H4 describes: a control nothing
	// exercises is not a control, and a denial nothing contrasts is not a
	// denial.
	It("P-control: the canary IS reachable from the ordinary e2e network", func() {
		out, err := probe("--network", e2eNetwork, canaryService+":"+canaryPort)
		Expect(err).NotTo(HaveOccurred(), "probe output:\n%s", out)
		Expect(out).To(ContainSubstring("ok"))
	})

	// What this probe proves, exactly: Docker's embedded DNS on sim-internal
	// does not resolve a container that is not on sim-internal. That is worth
	// asserting — it is the first thing a real exfiltration attempt hits — but
	// it is NOT the egress control, and the mutation transcript in
	// docs/proofs/simulator-containment.md says so in real output: with
	// `internal: false` this probe STILL PASSES, because the name still does
	// not resolve. P-deny-ip is the one that goes red. Do not read a green
	// P-deny-name as containment on its own.
	It("P-deny-name: the canary is NOT reachable by name from the sandbox's network namespace", func() {
		out, err := probe("--network", "container:"+simulatorID, canaryService+":"+canaryPort)
		Expect(err).To(HaveOccurred(),
			"the sandbox resolved and reached %s — the canary is on the sandbox's own network. probe output:\n%s", canaryService, out)
	})

	// The IP probe is the routing assertion, and it is the one that fails when
	// `internal: true` is removed. Dialling the literal address takes DNS out
	// of the question entirely, which is what separates "the sandbox could not
	// look the host up" from "the sandbox has no route to it".
	It("P-deny-ip: the canary is NOT reachable by literal IP from the sandbox's network namespace", func() {
		out, err := probe("--network", "container:"+simulatorID, canaryIP+":"+canaryPort)
		Expect(err).To(HaveOccurred(),
			"the sandbox reached %s by address — sim-internal routes off its own subnet. probe output:\n%s", canaryIP, out)
	})

	It("P-topology: the simulator sits on exactly one network, and the daemon reports it internal", func() {
		var attached map[string]json.RawMessage
		Expect(json.Unmarshal([]byte(dockerOut("inspect", "-f", "{{ json .NetworkSettings.Networks }}", simulatorID)), &attached)).To(Succeed())

		names := make([]string, 0, len(attached))
		for name := range attached {
			names = append(names, name)
		}
		Expect(names).To(ConsistOf(simNetwork),
			"the simulator must be attached to %s and nothing else; a second network is a second way out", simNetwork)

		// Read from the daemon, not from the compose file. compose_containment_test.go
		// already asserts the YAML says internal: true; this asserts the network
		// that actually exists is.
		Expect(strings.TrimSpace(dockerOut("network", "inspect", "-f", "{{ .Internal }}", simNetwork))).
			To(Equal("true"), "%s is not an internal network, so the sandbox has a route off it", simNetwork)

		// And the canary is not on it — the probes above would be vacuous if
		// the "outside" host were inside.
		var canaryNets map[string]json.RawMessage
		canaryID := strings.TrimSpace(dockerOut("compose", "-f", "docker-compose.e2e.yaml", "--profile", "sim", "ps", "-q", canaryService))
		Expect(json.Unmarshal([]byte(dockerOut("inspect", "-f", "{{ json .NetworkSettings.Networks }}", canaryID)), &canaryNets)).To(Succeed())
		Expect(canaryNets).NotTo(HaveKey(simNetwork), "the egress canary must never be attached to %s", simNetwork)
	})
})

// probe dials host:port from a throwaway container and returns its combined
// output. Reachability is expressed as the exit status: a connect that never
// completes is failure, which is why every probe carries its own timeout on
// both sides — busybox wget's -T, and a hard container timeout so a hung DNS
// lookup cannot stall the suite.
func probe(networkArgs ...string) (string, error) {
	target := networkArgs[len(networkArgs)-1]
	args := append([]string{"run", "--rm"}, networkArgs[:len(networkArgs)-1]...)
	args = append(args, probeImage, "wget", "-T", "3", "-q", "-O", "-", "http://"+target+"/")

	cmd := exec.Command("docker", args...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		defer close(done)
		out, err = cmd.CombinedOutput()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill() //nolint:errcheck // the timeout below is the assertion; a kill that fails still leaves an error
		return "probe timed out", fmt.Errorf("probe against %s timed out", target)
	}
	return string(out), err
}

// dockerOut runs a docker command that must succeed and returns its stdout.
func dockerOut(args ...string) string {
	GinkgoHelper()
	cmd := exec.Command("docker", args...)
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "docker %s failed: %s", strings.Join(args, " "), string(out))
	return string(out)
}

// CRITICAL-2's own probe graph, run end to end against the real simulator.
//
// The unit suite proves the transform rewrites the target
// (internal/simulate/transform_address_test.go). This proves the rewritten
// graph then RUNS: Alloy loads it, scrapes the harness's synthetic exporter
// instead of the host the graph named, and the capture receiver sees series.
// A transform that produced a safe config the sandbox could not load would pass
// every unit spec and deliver nothing, which is the deny-everything failure
// mode the keep-list model has to be defended against as much as the leak.
//
// It deliberately uses LITERAL targets rather than a wired discovery node, so
// it does not sit behind finding M13's renderer blocker — the same reason the
// original kill probe in docs/proofs/sandbox-sim-e2e.md was written that way.
var _ = Describe("Scenario sandbox-sim: a literal scrape target is re-pointed at the harness", Ordered, Label("sandbox-sim"), Label("sandbox-egress"), func() {
	// example.net, never resolvable, and the assertion is that the sandbox
	// never tries: the transform replaces it before the config is submitted.
	const namedHost = "internal-vault.example.net"

	var egressOrgID string

	BeforeAll(func() {
		Expect(adminClient).NotTo(BeNil(), "SynchronizedBeforeSuite must have logged in an admin client")
		var org struct {
			ID string `json:"id"`
		}
		status := adminClient.postJSON("/api/admin/orgs", map[string]string{
			"name":           "e2e-sandbox-egress-org",
			"display_name":   "E2E Sandbox-Egress Org",
			"admin_group_id": appAdminGroupID,
		}, &org)
		Expect(status).To(Equal(http.StatusCreated))
		egressOrgID = org.ID
	})

	It("runs the graph, discloses target_address_forced, and captures from the harness", func() {
		graph := map[string]any{
			"kind":           "alloy-graph/v1",
			"schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{
				{
					"id": "n1", "component": "prometheus.scrape", "label": "ssrf",
					"props": map[string]any{
						"job_name":        "ssrf",
						"scrape_interval": "5s", "scrape_timeout": "3s",
						"targets": []map[string]any{{
							"__address__": namedHost,
							"__scheme__":  "https",
							"job":         "x",
						}},
					},
				},
				{
					"id": "n2", "component": "prometheus.remote_write", "label": "sink",
					"props": map[string]any{
						"endpoint": []map[string]any{{"url": "https://prometheus.example.com/api/v1/write"}},
					},
				},
			},
			"edges": []map[string]any{
				{"id": "e1", "from": map[string]string{"node": "n1", "port": "forward_to"}, "to": map[string]string{"node": "n2", "port": "receiver"}},
			},
		}

		var created struct {
			RunID string `json:"run_id"`
		}
		status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/simulate/runs", egressOrgID), map[string]any{
			"org_id":           egressOrgID,
			"graph":            graph,
			"duration_seconds": 20,
		}, &created)
		Expect(status).To(Equal(http.StatusOK))
		Expect(created.RunID).NotTo(BeEmpty())

		Eventually(func() string {
			return getRunStatus(egressOrgID, created.RunID)
		}).WithTimeout(120 * time.Second).WithPolling(2 * time.Second).Should(Equal("completed"))

		run := getRun(egressOrgID, created.RunID)

		var kinds []string
		for _, r := range run.Rewrites {
			kinds = append(kinds, r.Kind)
		}
		Expect(kinds).To(ContainElement("target_address_forced"),
			"the user must be told their scrape target was replaced, not left to wonder why the sandbox scraped something else")

		// The proof it scraped the HARNESS: shepherd_sim_requests_total is the
		// synthetic exporter's own counter, and nothing at the host the graph
		// named could have produced it.
		var names []string
		for _, s := range run.CapturedSeries {
			names = append(names, s.Name)
		}
		Expect(names).To(ContainElement("shepherd_sim_requests_total"),
			"nothing captured — the re-pointed target must still be scrapeable, or deny-by-default has become deny-everything")
	})
})

// The honest test of the architecture, and the reason this file is labelled the
// control rather than a backstop.
//
// A discovery.relabel rule retargets a scrape at RUNTIME: `target_label =
// "__address__"` with a `replacement` assembled from regex captures over the
// source labels. Both of those paths are on discovery.relabel's sim_keep, and
// they have to be — relabel is the single thing users most want to simulate.
// The graph below therefore contains NO host-shaped token anywhere: the canary's
// address exists only after Alloy has joined four ordinary label values with a
// separator. Neither the transform nor CheckEndpoints can see it, and the unit
// suite says so out loud (internal/simulate/transform_address_test.go,
// "Transform: a runtime retarget is NOT contained by the transform").
//
// So this spec asserts the three halves that are actually true:
//
//	the graph is ACCEPTED — no transform refusal, proven against the live
//	pipeline rather than in a unit test, so a future static gate that starts
//	refusing it turns this red and forces the architecture doc to be updated;
//
//	the retarget really TAKES EFFECT at runtime — the sandbox's own captured
//	series come back labelled with the canary's address, not the harness's,
//	which is the transform's non-containment demonstrated by execution rather
//	than inferred from the rendered text;
//
//	the address it steers at is UNREACHABLE from the sandbox's own network
//	namespace, while the identical dial from the ordinary e2e network succeeds.
//
// The middle assertion was impossible until finding M13 was fixed — the
// renderer's `[...]` wrap round a `[]discovery.Target` reference made every
// discovery-to-scrape wire in the repo unrunnable, so the retargeted scrape
// could not execute at all (docs/proofs/sandbox-sim-e2e.md §1).
//
// Still deliberately NOT asserted: that the retargeted scrape MISSES, judged
// from captured series. It cannot be judged there — a denied scrape and a
// scrape that reached the canary produce the same five series either way
// (`up` plus Prometheus's four scrape internals; the canary answers "ok",
// which is not parseable Prometheus text). Reachability is judged by the
// out-of-band probes below, dialled from the sandbox's own namespace.
var _ = Describe("Scenario sandbox-sim: a runtime retarget is accepted by the transform and denied by the network",
	Ordered, Label("sandbox-sim"), Label("sandbox-egress"), func() {
		const (
			canaryService = "egress-canary"
			canaryPort    = "8080"
			e2eNetwork    = "shepherd-e2e"
		)

		var (
			simulatorID  string
			canaryIP     string
			canaryOctets []string
			retargetOrg  string
		)

		BeforeAll(func() {
			simulatorID = strings.TrimSpace(dockerOut("compose", "-f", "docker-compose.e2e.yaml", "--profile", "sim", "ps", "-q", "simulator"))
			Expect(simulatorID).NotTo(BeEmpty(), "the simulator container must be up")

			canaryID := strings.TrimSpace(dockerOut("compose", "-f", "docker-compose.e2e.yaml", "--profile", "sim", "ps", "-q", canaryService))
			Expect(canaryID).NotTo(BeEmpty(), "the egress-canary container must be up")
			canaryIP = strings.TrimSpace(dockerOut("inspect", "-f",
				fmt.Sprintf("{{ (index .NetworkSettings.Networks %q).IPAddress }}", e2eNetwork), canaryID))

			canaryOctets = strings.Split(canaryIP, ".")
			Expect(canaryOctets).To(HaveLen(4), "the canary must have a dotted-quad address on %s, got %q", e2eNetwork, canaryIP)

			Expect(adminClient).NotTo(BeNil(), "SynchronizedBeforeSuite must have logged in an admin client")
			var org struct {
				ID string `json:"id"`
			}
			status := adminClient.postJSON("/api/admin/orgs", map[string]string{
				"name":           "e2e-sandbox-retarget-org",
				"display_name":   "E2E Sandbox-Retarget Org",
				"admin_group_id": appAdminGroupID,
			}, &org)
			Expect(status).To(Equal(http.StatusCreated))
			retargetOrg = org.ID
		})

		It("accepts the retarget graph: no gate in Shepherd or the simulator refuses it", func() {
			graph := map[string]any{
				"kind":           "alloy-graph/v1",
				"schema_version": "alloy-v1.18.1",
				"nodes": []map[string]any{
					{
						"id": "n1", "component": "discovery.relabel", "label": "retarget",
						"props": map[string]any{
							// The seed address is loopback:1 — inert, and the
							// transform forces it to the harness anyway. The
							// canary's address is carried as four ordinary label
							// values that name no host on their own.
							"targets": []map[string]any{{
								"__address__": "127.0.0.1:1",
								"o1":          canaryOctets[0], "o2": canaryOctets[1],
								"o3": canaryOctets[2], "o4": canaryOctets[3],
							}},
							"rule": []map[string]any{{
								"source_labels": []string{"o1", "o2", "o3", "o4"},
								"separator":     ".",
								"regex":         "(.*)",
								"target_label":  "__address__",
								"replacement":   "${1}:" + canaryPort,
								"action":        "replace",
							}},
						},
					},
					{
						"id": "n2", "component": "prometheus.scrape", "label": "scrape",
						"props": map[string]any{"job_name": "retarget", "scrape_interval": "2s", "scrape_timeout": "1s"},
					},
					{
						"id": "n3", "component": "prometheus.remote_write", "label": "sink",
						"props": map[string]any{
							"endpoint": []map[string]any{{"url": "https://prometheus.example.com/api/v1/write"}},
						},
					},
				},
				"edges": []map[string]any{
					{"id": "e1", "from": map[string]string{"node": "n1", "port": "output"}, "to": map[string]string{"node": "n2", "port": "targets"}},
					{"id": "e2", "from": map[string]string{"node": "n2", "port": "forward_to"}, "to": map[string]string{"node": "n3", "port": "receiver"}},
				},
			}

			var created struct {
				RunID string `json:"run_id"`
			}
			status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/simulate/runs", retargetOrg), map[string]any{
				"org_id":           retargetOrg,
				"graph":            graph,
				"duration_seconds": 15,
			}, &created)
			Expect(status).To(Equal(http.StatusOK))
			Expect(created.RunID).NotTo(BeEmpty())

			Eventually(func() string {
				return getRunStatus(retargetOrg, created.RunID)
			}).WithTimeout(120*time.Second).WithPolling(2*time.Second).Should(Equal("completed"),
				"the retarget graph must RUN, not merely be accepted — this used to be "+
					"BeElementOf(\"completed\",\"failed\") because finding M13 made the "+
					"discovery→scrape wire unrunnable, and a spec that tolerates failure "+
					"cannot tell non-containment from a broken pipeline")

			run := getRun(retargetOrg, created.RunID)

			// "cannot_stub" is the bucket every TransformErrors refusal lands in
			// (internal/simulate/worker.go). Its absence is the assertion: the
			// transform did not, and per the architecture must not pretend to,
			// contain this graph.
			Expect(run.ErrorCode).NotTo(Equal("cannot_stub"),
				"the transform refused a runtime-retarget graph (%s). If that is intended, the reachability control moved back "+
					"into the transform and docs/proofs/simulator-containment.md, VB-1 §6.4 and the unit spec "+
					"\"Transform: a runtime retarget is NOT contained by the transform\" all have to change with it",
				run.ErrorMessage)

			// The disclosure still tells the user their seed target was moved —
			// the target_set class earns its place even though it is not the
			// reachability control.
			var kinds []string
			for _, r := range run.Rewrites {
				kinds = append(kinds, r.Kind)
			}
			Expect(kinds).To(ContainElement("target_address_forced"))

			// And the disclosure is not the last word: the rule overrode the
			// forced address at runtime, which is visible in the sandbox's OWN
			// captured series. Every series carries instance=<canary>:8080 —
			// the address the transform never saw — instead of the harness's
			// synthetic exporter that target_address_forced pointed at.
			//
			// This is the non-containment claim proven by execution. If a
			// future change made the transform actually contain the retarget,
			// instance would read simulator:9111 and this goes red — which is
			// the correct outcome, and means the architecture note in VB-1
			// §6.4 and docs/proofs/simulator-containment.md must change too.
			Expect(run.CapturedSeries).NotTo(BeEmpty(),
				"the retargeted scrape produced no series at all, so this spec cannot tell whether the retarget took effect")
			for _, s := range run.CapturedSeries {
				Expect(s.Labels).To(HaveKeyWithValue("instance", canaryIP+":"+canaryPort),
					"series %s carries instance=%q; the runtime relabel rule was expected to steer every scrape at the canary",
					s.Name, s.Labels["instance"])
			}
		})

		// The control, dialled at the exact address the rule above assembles.
		It("P-retarget-control: that address IS reachable from the ordinary e2e network", func() {
			out, err := probe("--network", e2eNetwork, canaryIP+":"+canaryPort)
			Expect(err).NotTo(HaveOccurred(), "probe output:\n%s", out)
			Expect(out).To(ContainSubstring("ok"))
		})

		It("P-retarget-deny: and is NOT reachable from the sandbox's own network namespace", func() {
			out, err := probe("--network", "container:"+simulatorID, canaryIP+":"+canaryPort)
			Expect(err).To(HaveOccurred(),
				"the sandbox reached %s — the address a relabel rule can steer it at is routable from sim-internal, "+
					"and nothing else in this design stops it. probe output:\n%s", canaryIP, out)
		})
	})
