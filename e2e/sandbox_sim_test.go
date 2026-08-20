//go:build e2e

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Scenario `sandbox-sim` (VB-1 design doc §7.8 item 2, §11 item 9, §13
// milestone 7) — an S3 sandbox run against the REAL simulator container,
// not a mock. Gated behind `--profile sim` / `make e2e-sim`, which is why
// this whole file carries Label("sandbox-sim") and `make e2e` filters it
// out: bringing the simulator up costs a real Alloy WAL replay (~6s) plus
// scrape+remote_write settle time per run, a budget the default suite does
// not carry.
//
// It is deliberately self-contained rather than reusing the shared
// `orgID`/"1. Registration" spec above: SynchronizedBeforeSuite (adminClient)
// always runs, but --label-filter=sandbox-sim skips every unlabeled It,
// including the one that populates the package-level orgID.
//
// THIS SPEC WAS RED FOR A REAL REASON AND IS NOW GREEN — do not "simplify" the
// graph below back to a literal target set. It submits a wired
// discovery → scrape → remote_write pipeline precisely because that wiring was
// what finding M13 broke: internal/visual/render.go used to wrap every wired
// "targets" input in a `[...]` list literal, which `alloy validate` accepts and
// `alloy run` refuses with `target::ConvertFrom: conversion from
// '[]discovery.Target' is not supported`. Fixed in render.go's refValue;
// docs/proofs/sandbox-sim-e2e.md §1 has the red and green transcripts against a
// live stack. A graph that avoids the wire would pass without exercising it.
var _ = Describe("Scenario sandbox-sim: S3 sandbox run against the real simulator", Ordered, Label("sandbox-sim"), func() {
	var (
		simOrgID string
		runID    string
	)

	BeforeAll(func() {
		Expect(adminClient).NotTo(BeNil(), "SynchronizedBeforeSuite must have logged in an admin client")

		var org struct {
			ID string `json:"id"`
		}
		status := adminClient.postJSON("/api/admin/orgs", map[string]string{
			"name":           "e2e-sandbox-sim-org",
			"display_name":   "E2E Sandbox-Sim Org",
			"admin_group_id": appAdminGroupID,
		}, &org)
		Expect(status).To(Equal(http.StatusCreated))
		simOrgID = org.ID
		Expect(simOrgID).NotTo(BeEmpty())
	})

	// minimalScrapeGraph mirrors internal/visual/testdata/corpus/minimal-scrape.graph.json
	// byte-for-byte in shape (discovery.kubernetes -> prometheus.scrape ->
	// prometheus.remote_write) — the same corpus graph the transform unit
	// suite and the design doc's own scenario text (§7.8 item 2) name. Kept
	// as a literal rather than read from disk: the e2e binary's working
	// directory is not guaranteed to be the repo root, and a graph this
	// small is cheaper to keep in sync by inspection than to wire a file
	// read for. The scrape timing props differ from the corpus graph's for
	// the reason stated on them below; the wiring, which is what this
	// scenario tests, does not.
	minimalScrapeGraph := map[string]any{
		"kind":           "alloy-graph/v1",
		"schema_version": "alloy-v1.18.1",
		"nodes": []map[string]any{
			{
				"id": "n1", "component": "discovery.kubernetes", "label": "k8s",
				"props": map[string]any{"role": "pod"},
			},
			{
				"id": "n2", "component": "prometheus.scrape", "label": "app",
				// scrape_interval MUST stay well under the run's
				// duration_seconds below, and scrape_timeout MUST stay under
				// scrape_interval. Both are load-bearing, both were learned
				// the hard way against the live stack:
				//
				//   - at the corpus graph's 30s interval, an 18s run ends
				//     before Prometheus's jittered first scrape about half the
				//     time — observed as this suite's "captured series include
				//     the synthetic exporter's counter" spec passing on one
				//     clean-stack run and failing on the next with zero series.
				//     A capture assertion that flips on scrape jitter proves
				//     nothing on the run where it happens to pass.
				//   - Alloy's default scrape_timeout is 10s, and it refuses to
				//     BUILD a scrape whose timeout exceeds its interval
				//     ("scrape_timeout (10s) greater than scrape_interval"),
				//     which fails the whole run at load. Lowering the interval
				//     without also lowering the timeout trades a flake for a
				//     hard red.
				"props": map[string]any{"job_name": "app", "scrape_interval": "5s", "scrape_timeout": "2s"},
			},
			{
				"id": "n3", "component": "prometheus.remote_write", "label": "sink",
				"props": map[string]any{
					"endpoint": []map[string]any{{"url": "https://prometheus.example.com/api/v1/write"}},
				},
			},
		},
		"edges": []map[string]any{
			{"id": "e1", "from": map[string]string{"node": "n1", "port": "targets"}, "to": map[string]string{"node": "n2", "port": "targets"}},
			{"id": "e2", "from": map[string]string{"node": "n2", "port": "forward_to"}, "to": map[string]string{"node": "n3", "port": "receiver"}},
		},
	}

	It("submits the minimal-scrape graph and reaches completed within 90s", func() {
		var created struct {
			RunID string `json:"run_id"`
		}
		// org_id repeats the URL path segment in the body: SimulateHandler.CreateRun
		// (internal/mgmtapi/simulate.go) pre-sets req.OrgId from chi.URLParam, but
		// protojson.Unmarshal resets the message before merging the body, so an
		// omitted org_id lands as "" — matching the convention rpc_simulate_run_test.go's
		// minimalRunGraph and simulate_run_rest_test.go already use.
		status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/simulate/runs", simOrgID), map[string]any{
			"org_id":           simOrgID,
			"graph":            minimalScrapeGraph,
			"duration_seconds": 18,
		}, &created)
		Expect(status).To(Equal(http.StatusOK), "CreateRun must succeed")
		Expect(created.RunID).NotTo(BeEmpty())
		runID = created.RunID

		Eventually(func() string {
			return getRunStatus(simOrgID, runID)
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Equal("completed"))
	})

	It("captured series include the synthetic exporter's counter carrying a relabel-produced label", func() {
		run := getRun(simOrgID, runID)
		Expect(run.CapturedSeries).NotTo(BeEmpty(),
			"nothing captured — component health is NOT evidence of delivery (VB-1 §6.4); "+
				"this is the assertion that must key on captured series, never on health")

		var match *capturedSeries
		for i := range run.CapturedSeries {
			if run.CapturedSeries[i].Name == "shepherd_sim_requests_total" {
				match = &run.CapturedSeries[i]
				break
			}
		}
		Expect(match).NotTo(BeNil(), "expected the synthetic exporter's counter among captured series, got %+v", run.CapturedSeries)
		Expect(match.Labels).To(HaveKeyWithValue("job", "app"), "job label comes from prometheus.scrape's own job_name prop")
		// "instance" is not a label the synthetic exporter emits (it exposes
		// only path/method) and not one the authored graph set anywhere —
		// Prometheus's own scrape-time relabeling is what promotes
		// __address__ (the discovery.relabel stub's target label) into it.
		// Its presence is the "relabel-produced label" the scenario checks.
		Expect(match.Labels).To(HaveKey("instance"))
		Expect(match.Labels["instance"]).NotTo(BeEmpty())
	})

	It("component health is all-green", func() {
		run := getRun(simOrgID, runID)
		Expect(run.ComponentHealth).NotTo(BeEmpty())
		for _, c := range run.ComponentHealth {
			Expect(c.HealthState).To(Equal("healthy"), "component %s (%s) reported %q: %s", c.NodeLabel, c.Component, c.HealthState, c.Message)
		}
	})

	// The stderr tail is the only place a user can see what the sandbox Alloy
	// itself said, and it is what makes a failed run diagnosable rather than
	// just red (finding H8 shipped the UI for it). Asserting a line that only
	// real Alloy emits — not merely "not empty" — is what keeps this from
	// passing on an empty string, a copied request echo, or Shepherd's own log.
	It("the stderr tail carries the sandbox Alloy's own output", func() {
		run := getRun(simOrgID, runID)
		Expect(run.StderrTail).NotTo(BeEmpty())
		Expect(run.StderrTail).To(ContainSubstring("Alloy is running"),
			"expected the sandbox Alloy's own startup line in the tail, got: %s", run.StderrTail)
	})

	It("the rewrite disclosure lists exactly the expected rewrites: one discovery stub, one destination rewrite", func() {
		run := getRun(simOrgID, runID)
		var kinds []string
		for _, r := range run.Rewrites {
			kinds = append(kinds, r.Kind)
		}
		Expect(kinds).To(ConsistOf("discovery_stubbed", "destination_endpoint"),
			"minimal-scrape has exactly one Sources node (discovery.kubernetes) and one Destinations node (prometheus.remote_write); "+
				"prometheus.scrape itself is neither and must not appear in the disclosure")
	})

	It("a graph that captures nothing still completes with a populated health tab, not a 500", func() {
		// prometheus.scrape pointed at zero targets: a legal graph — the
		// transform has nothing to stub here (no Sources node), and
		// prometheus.remote_write is still the one Destinations node, but
		// nothing ever reaches it. Confirmed against the real simulator
		// (docs/proofs/sandbox-sim-e2e.md): 0 targets does NOT flip Alloy's
		// component health to "unhealthy" — it stays healthy and idle, which
		// is itself the sharp edge VB-1 §6.4 calls out ("component health is
		// NOT evidence of delivery"). The assertion below is what that edge
		// actually requires: the run must still complete with a populated
		// health tab, never a 500 — not "the health state must say
		// unhealthy", which real Alloy does not do here.
		unhealthyGraph := map[string]any{
			"kind":           "alloy-graph/v1",
			"schema_version": "alloy-v1.18.1",
			"nodes": []map[string]any{
				{
					"id": "n1", "component": "prometheus.scrape", "label": "empty",
					"props": map[string]any{"job_name": "empty", "targets": []map[string]any{}},
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
		status := adminClient.postJSON(fmt.Sprintf("/api/orgs/%s/simulate/runs", simOrgID), map[string]any{
			"org_id":           simOrgID,
			"graph":            unhealthyGraph,
			"duration_seconds": 18,
		}, &created)
		Expect(status).To(Equal(http.StatusOK), "CreateRun must succeed — an eventually-unhealthy component is not a request error")
		Expect(created.RunID).NotTo(BeEmpty())
		failRunID := created.RunID

		Eventually(func() string {
			return getRunStatus(simOrgID, failRunID)
		}).WithTimeout(90*time.Second).WithPolling(2*time.Second).Should(Equal("completed"),
			"a component reporting an unhealthy state must still be a completed run, never a transport error")

		run := getRun(simOrgID, failRunID)
		Expect(run.ComponentHealth).NotTo(BeEmpty(), "the health tab must show a state for every component, healthy or not")
	})
})

// capturedSeries/componentHealth/rewrite/simulateRunView mirror the JSON
// shim's snake_case field names (mgmtapi.MarshalOpts: UseProtoNames,
// EmitUnpopulated) for shepherd.mgmt.v1.SimulateRun and its nested messages.
type capturedSeries struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

type componentHealth struct {
	NodeID      string `json:"node_id"`
	NodeLabel   string `json:"node_label"`
	Component   string `json:"component"`
	HealthState string `json:"health_state"`
	Message     string `json:"message"`
}

type rewrite struct {
	Kind string `json:"kind"`
}

type simulateRunView struct {
	Status          string            `json:"status"`
	ErrorCode       string            `json:"error_code"`
	ErrorMessage    string            `json:"error_message"`
	StderrTail      string            `json:"stderr_tail"`
	CapturedSeries  []capturedSeries  `json:"captured_series"`
	ComponentHealth []componentHealth `json:"component_health"`
	Rewrites        []rewrite         `json:"rewrites"`
}

func getRun(orgID, runID string) simulateRunView {
	GinkgoHelper()
	var run simulateRunView
	adminClient.getJSON(fmt.Sprintf("/api/orgs/%s/simulate/runs/%s", orgID, runID), &run)
	return run
}

func getRunStatus(orgID, runID string) string {
	GinkgoHelper()
	resp, err := adminClient.do("GET", fmt.Sprintf("/api/orgs/%s/simulate/runs/%s", orgID, runID), nil)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close() //nolint:errcheck // test cleanup
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var run simulateRunView
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return ""
	}
	return run.Status
}
