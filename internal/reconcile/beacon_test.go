package reconcile

import (
	"strings"
	"testing"
	"time"
)

// TestObserveBeacon_RowMissingFromLatestReportIsNotRunning pins the
// walkthrough case: pipe_k8smon_metrics was served, then disabled; Alloy
// reloaded and stopped reporting it, so its row stopped being refreshed while
// the instance's live rows kept refreshing every scrape. A row the instance's
// latest report did not refresh is no longer running and must not be observed.
func TestObserveBeacon_RowMissingFromLatestReportIsNotRunning(t *testing.T) {
	latest := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []BeaconRow{
		{Instance: "pod-a:12345", Component: "pipe_app_logs", Healthy: true, LastSeen: latest},
		{Instance: "pod-a:12345", Component: "pipe_app_traces", Healthy: true, LastSeen: latest.Add(-2 * time.Second)},
		{Instance: "pod-a:12345", Component: "pipe_k8smon_metrics", Healthy: true, LastSeen: latest.Add(-4*time.Minute - 22*time.Second)},
	}
	got := ObserveBeacon(rows, latest.Add(-5*time.Minute))
	if names := strings.Join(componentNames(got), ","); names != "pipe_app_logs,pipe_app_traces" {
		t.Fatalf("want only the rows the latest report refreshed, got %v", names)
	}
}

// TestObserveBeacon_LatestReportIsPerInstance pins that the latest report is
// judged per instance: an instance that reported less recently than its
// sibling keeps its own rows.
func TestObserveBeacon_LatestReportIsPerInstance(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []BeaconRow{
		{Instance: "pod-a:12345", Component: "pipe_app_logs", Healthy: true, LastSeen: now},
		{Instance: "pod-b:12345", Component: "pipe_old", Healthy: false, LastSeen: now.Add(-90 * time.Second)},
	}
	got := ObserveBeacon(rows, now.Add(-5*time.Minute))
	if names := strings.Join(componentNames(got), ","); names != "pipe_app_logs,pipe_old" {
		t.Fatalf("want each instance's latest report kept, got %+v", got)
	}
	if got[1].Healthy {
		t.Error("want Healthy carried through from the row")
	}
}

// TestObserveBeacon_SilentInstanceKeepsLastReportFlaggedStale pins the other
// half: an instance that stopped reporting altogether keeps its last report
// (still positive evidence it WAS running) until the sweeper expires it,
// flagged stale once past the cutoff.
func TestObserveBeacon_SilentInstanceKeepsLastReportFlaggedStale(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []BeaconRow{
		{Instance: "gone:1", Component: "pipe_a", Healthy: true, LastSeen: now.Add(-6 * time.Minute)},
		{Instance: "gone:1", Component: "pipe_b", Healthy: true, LastSeen: now.Add(-6*time.Minute - 3*time.Second)},
	}
	got := ObserveBeacon(rows, now.Add(-5*time.Minute))
	if len(got) != 2 || !got[0].Stale || !got[1].Stale {
		t.Fatalf("want both rows of the silent instance's last report, flagged stale, got %+v", got)
	}
}

func componentNames(cs []ObservedComponent) []string {
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].ControllerPath
	}
	return out
}
