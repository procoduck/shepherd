package simsvc

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// healthTracker is what makes a sandbox run report "this component was broken"
// instead of "this component exited" — VB-1 §6.4's requirement that a run whose
// component went unhealthy still COMPLETES with that state in the health tab,
// rather than failing the request. The stickiness it implements is not
// observable from the run API against real Alloy today (measured: the S3
// transform stubs or removes every component that reports unhealthy at
// runtime — discovery/log sources are replaced by fixtures, local.file and
// remote.http are removed as secret sources, and the otelcol wrapper reports
// "healthy" even when its receiver fails to bind; see
// docs/proofs/sandbox-sim-e2e.md §4). These specs pin it at the level where it
// IS reachable, so the branch is not carried untested on the strength of an
// argument.
var _ = Describe("healthTracker", func() {
	comp := func(id, state, msg string) alloyComponent {
		var c alloyComponent
		c.LocalID = id
		c.Health.State = state
		c.Health.Message = msg
		return c
	}

	It("keeps an unhealthy observation when shutdown flips the component to a benign state", func() {
		t := newHealthTracker()
		t.observe([]alloyComponent{comp("prometheus.remote_write.sink", "healthy", "started component")})
		t.observe([]alloyComponent{comp("prometheus.remote_write.sink", "unhealthy", "remote write failed")})
		// Teardown: Alloy reports every component "exited" as the run ends. A
		// run that was broken for most of its life must not be reported clean
		// because of the last poll before shutdown.
		t.observe([]alloyComponent{comp("prometheus.remote_write.sink", "exited", "component shut down")})

		snap := t.snapshot(map[string]string{"prometheus.remote_write.sink": "n2"})
		Expect(snap).To(HaveLen(1))
		Expect(snap[0].NodeID).To(Equal("n2"))
		Expect(snap[0].Health).To(Equal("unhealthy"))
		Expect(snap[0].Message).To(Equal("remote write failed"))
	})

	It("reports the latest state for a component that was never unhealthy", func() {
		t := newHealthTracker()
		t.observe([]alloyComponent{comp("prometheus.scrape.app", "healthy", "started component")})
		t.observe([]alloyComponent{comp("prometheus.scrape.app", "exited", "component shut down")})

		snap := t.snapshot(map[string]string{"prometheus.scrape.app": "n1"})
		Expect(snap).To(HaveLen(1))
		// Not sticky in the other direction: only "unhealthy" is held, so a
		// healthy component still reports its real final state.
		Expect(snap[0].Health).To(Equal("exited"))
	})

	It("reports every component in first-seen order, and a component with no node mapping still appears", func() {
		t := newHealthTracker()
		t.observe([]alloyComponent{
			comp("discovery.relabel.k8s", "healthy", "started component"),
			comp("prometheus.scrape.app", "healthy", "started component"),
		})
		// A component the transform added has no authored node behind it; it
		// must still be reported rather than dropped, otherwise the health tab
		// silently omits whatever the sandbox actually ran.
		snap := t.snapshot(map[string]string{"prometheus.scrape.app": "n1"})
		Expect(snap).To(HaveLen(2))
		Expect(snap[0].LocalID).To(Equal("discovery.relabel.k8s"))
		Expect(snap[0].NodeID).To(BeEmpty())
		Expect(snap[1].LocalID).To(Equal("prometheus.scrape.app"))
		Expect(snap[1].NodeID).To(Equal("n1"))
	})
})
