package reconcile

import "time"

// LatestReportTolerance is how far behind its instance's newest row a
// beacon_inventory row may be and still count as part of that instance's
// latest report. The beacon handler stamps last_seen = now() on every row a
// write refreshes, but one scrape's series can reach Shepherd in more than one
// remote_write request (the queue shards series and flushes each shard on its
// own batch_send_deadline, 5s by default), so the rows of one report land a
// few seconds apart. The tolerance absorbs that spread and is well under the
// baseline's scrape_interval (60s by default), so a row a report stopped
// carrying drops out after one or two scrapes.
const LatestReportTolerance = 15 * time.Second

// BeaconRow is one beacon_inventory row, reduced to what reconciliation reads
// — restated here so this package stays free of internal/store.
type BeaconRow struct {
	Instance  string // the reporting Alloy's `instance` label
	Component string // the managed pipeline key, e.g. "pipe_app_logs"
	Healthy   bool
	LastSeen  time.Time
}

// ObserveBeacon turns a collector's beacon rows into the components it is
// running now. A row counts only if its instance's latest report refreshed it:
// within LatestReportTolerance of the newest last_seen among that same
// instance's rows. The beacon only upserts what a report carries — a pipeline
// that was disabled and dropped by a reload simply stops being refreshed while
// its siblings keep refreshing — so a row that fell behind is a component no
// longer running, not a running one; the sweeper deletes it later.
//
// The rule is relative to the instance's own newest row, not a fixed age, so
// it holds at any scrape interval. An instance that stopped reporting
// altogether keeps its last report (positive evidence it WAS running, until
// the sweeper expires it); a row older than staleBefore is flagged Stale.
// One limit: an instance whose every managed pipeline went away writes no
// rows at all, so its last report keeps counting until staleBefore and then
// the sweeper — there is no newer row to compare against.
func ObserveBeacon(rows []BeaconRow, staleBefore time.Time) []ObservedComponent {
	newest := make(map[string]time.Time, len(rows))
	for i := range rows {
		if t, ok := newest[rows[i].Instance]; !ok || rows[i].LastSeen.After(t) {
			newest[rows[i].Instance] = rows[i].LastSeen
		}
	}
	var out []ObservedComponent
	for i := range rows {
		r := rows[i]
		if r.LastSeen.Before(newest[r.Instance].Add(-LatestReportTolerance)) {
			continue
		}
		out = append(out, ObservedComponent{
			ControllerPath: r.Component,
			Healthy:        r.Healthy,
			Stale:          r.LastSeen.Before(staleBefore),
		})
	}
	return out
}
