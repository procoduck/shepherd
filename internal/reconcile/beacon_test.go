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

// TestObserveBeacon_SolePipelineDroppedIsNotRunning pins the 0.21 re-walk
// case: the only pipeline on a collector was disabled, so after Alloy
// reloaded its beacon writes carried no pipeline series and refreshed no row.
// The dropped row stayed the instance's newest, and judged against the
// newest row it read as running until the sweeper deleted it. Judged against
// when the instance last reported, it is not running.
func TestObserveBeacon_SolePipelineDroppedIsNotRunning(t *testing.T) {
	reported := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []BeaconRow{
		{Instance: "pod-a:12345", Component: "pipe_k8smon_logs", Healthy: true, LastSeen: reported.Add(-70 * time.Second), ReportedAt: reported},
	}
	if got := ObserveBeacon(rows, reported.Add(-5*time.Minute)); len(got) != 0 {
		t.Fatalf("want no running component once the instance reported without it, got %+v", got)
	}
}

// TestObserveBeacon_ReportTimeKeepsRefreshedRows pins that a row the latest
// report refreshed still counts when the report time is known: within
// LatestReportTolerance of it, before or after (the handler throttles the
// stamp, so it may trail a row it just refreshed by a few seconds).
func TestObserveBeacon_ReportTimeKeepsRefreshedRows(t *testing.T) {
	reported := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []BeaconRow{
		{Instance: "pod-a:12345", Component: "pipe_a", Healthy: true, LastSeen: reported.Add(-3 * time.Second), ReportedAt: reported},
		{Instance: "pod-a:12345", Component: "pipe_b", Healthy: true, LastSeen: reported.Add(4 * time.Second), ReportedAt: reported},
		{Instance: "pod-a:12345", Component: "pipe_gone", Healthy: true, LastSeen: reported.Add(-61 * time.Second), ReportedAt: reported},
	}
	got := ObserveBeacon(rows, reported.Add(-5*time.Minute))
	if names := strings.Join(componentNames(got), ","); names != "pipe_a,pipe_b" {
		t.Fatalf("want the refreshed rows only, got %v", names)
	}
}

// TestObserveBeacon_UnknownReportTimeFallsBackToNewestRow pins the rolling-
// upgrade fallback: rows whose report time is unknown (written before
// migration 0046, or by an older replica) are judged against the instance's
// newest row, as before; a known report time on any of the instance's rows
// is the instance's, and never hides a row newer than it.
func TestObserveBeacon_UnknownReportTimeFallsBackToNewestRow(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	rows := []BeaconRow{
		// No report time anywhere: the sole row is the newest, so it counts.
		{Instance: "old:1", Component: "pipe_sole", Healthy: true, LastSeen: now.Add(-70 * time.Second)},
		// Mixed: one row stamped, the other not — the stamp is the instance's.
		{Instance: "mixed:1", Component: "pipe_live", Healthy: true, LastSeen: now, ReportedAt: now},
		{Instance: "mixed:1", Component: "pipe_gone", Healthy: true, LastSeen: now.Add(-70 * time.Second)},
		// A stamp older than the newest row (an older replica refreshed the
		// rows since) never hides what the newest row shows.
		{Instance: "lagging:1", Component: "pipe_new", Healthy: true, LastSeen: now, ReportedAt: now.Add(-2 * time.Minute)},
	}
	got := ObserveBeacon(rows, now.Add(-5*time.Minute))
	if names := strings.Join(componentNames(got), ","); names != "pipe_sole,pipe_live,pipe_new" {
		t.Fatalf("want the newest-row fallback where the report time is unknown, got %v", names)
	}
}

// TestInventoryCurrent_ReportRecordOutlivesSweptRows pins the v0.20.0
// follow-up: a collector whose only pipeline was dropped kept reporting, the
// sweeper deleted its rows by last_seen, and the tab read "unknown". The
// instance's last-report record (0048) is evidence on its own.
func TestInventoryCurrent_ReportRecordOutlivesSweptRows(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	staleBefore := now.Add(-5 * time.Minute)
	if InventoryCurrent(nil, nil, staleBefore) {
		t.Fatal("no rows and no record: want not current (unknown)")
	}
	if !InventoryCurrent(nil, map[string]time.Time{"pod-a:12345": now.Add(-30 * time.Second)}, staleBefore) {
		t.Fatal("a recent record with every row swept: want current")
	}
	if InventoryCurrent(nil, map[string]time.Time{"pod-a:12345": now.Add(-6 * time.Minute)}, staleBefore) {
		t.Fatal("a record older than the stale window: want not current")
	}
	stale := []BeaconRow{{Instance: "pod-a:12345", Component: "pipe_x", LastSeen: now.Add(-6 * time.Minute)}}
	if InventoryCurrent(stale, nil, staleBefore) {
		t.Fatal("only rows older than the stale window: want not current")
	}
}

// TestWithReports_RecordDropsARowTheReportNoLongerCarries pins that the
// record moves the instance's last report on like the 0046 stamp does, and
// that a missing record changes nothing (rolling-upgrade fallback).
func TestWithReports_RecordDropsARowTheReportNoLongerCarries(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	staleBefore := now.Add(-5 * time.Minute)
	rows := []BeaconRow{{Instance: "pod-a:12345", Component: "pipe_ghost", Healthy: true, LastSeen: now.Add(-70 * time.Second)}}

	if got := ObserveBeacon(WithReports(rows, nil), staleBefore); len(got) != 1 {
		t.Fatalf("no record: want the newest row still counted (pre-0048 rule), got %v", got)
	}
	if got := ObserveBeacon(WithReports(rows, map[string]time.Time{"pod-b:1": now}), staleBefore); len(got) != 1 {
		t.Fatalf("another instance's record: want this instance unaffected, got %v", got)
	}
	if got := ObserveBeacon(WithReports(rows, map[string]time.Time{"pod-a:12345": now}), staleBefore); len(got) != 0 {
		t.Fatalf("a newer record: want the row the report no longer carries dropped, got %v", got)
	}
	if !rows[0].ReportedAt.IsZero() {
		t.Fatal("WithReports must not mutate its input")
	}
}

func componentNames(cs []ObservedComponent) []string {
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].ControllerPath
	}
	return out
}
