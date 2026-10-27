package reconcile

import "time"

// LatestReportTolerance is how far behind its instance's last report (see
// ObserveBeacon) a beacon_inventory row may be and still count as part of
// that report. The beacon handler stamps last_seen = now() on every row a
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
	// ReportedAt is when the instance last reported (beacon_inventory.
	// instance_reported_at), stamped on every accepted write, including one
	// that carries no pipeline series. Zero when unknown: a row last written
	// before migration 0046, or by an older replica.
	ReportedAt time.Time
}

// ObserveBeacon turns a collector's beacon rows into the components it is
// running now. A row counts only if its instance's latest report refreshed it:
// within LatestReportTolerance of when that instance last reported. The
// beacon only upserts what a report carries — a pipeline that was disabled
// and dropped by a reload simply stops being refreshed — so a row that fell
// behind is a component no longer running, not a running one; the sweeper
// deletes it later.
//
// When the instance last reported is LastReport: its newest ReportedAt (the
// handler stamps every accepted write, including one carrying no pipeline
// series, so a collector whose only pipeline was dropped still moves it on),
// or its newest LastSeen when that is later or no row is stamped (rows
// written before migration 0046, or by an older replica mid-upgrade — the
// rule then is the pre-0046 one: relative to the newest row).
//
// The rule is relative to the instance's own report, not a fixed age, so it
// holds at any scrape interval. An instance that stopped reporting
// altogether keeps its last report (positive evidence it WAS running, until
// the sweeper expires it); a row older than staleBefore is flagged Stale.
func ObserveBeacon(rows []BeaconRow, staleBefore time.Time) []ObservedComponent {
	last := make(map[string]time.Time, len(rows))
	for i := range rows {
		last[rows[i].Instance] = latest(last[rows[i].Instance], LastReport(rows[i]))
	}
	var out []ObservedComponent
	for i := range rows {
		r := rows[i]
		if r.LastSeen.Before(last[r.Instance].Add(-LatestReportTolerance)) {
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

// LastReport is the latest time a row shows its instance reporting: the
// later of the row's own refresh and the instance's report stamp on it (zero
// when unstamped).
func LastReport(r BeaconRow) time.Time {
	return latest(r.LastSeen, r.ReportedAt)
}

// WithReports folds each instance's last-report record (beacon_instance_
// reports, 0048) into its rows' ReportedAt, keeping the later of the record
// and the row's own 0046 stamp. A missing record leaves the row as it was — a
// Shepherd mid-upgrade, or an older replica that never wrote one — so the rule
// falls back to the 0046 stamp and then to the newest row, as before. The
// input is not modified.
func WithReports(rows []BeaconRow, reports map[string]time.Time) []BeaconRow {
	if len(reports) == 0 {
		return rows
	}
	out := make([]BeaconRow, len(rows))
	for i := range rows {
		out[i] = rows[i]
		out[i].ReportedAt = latest(out[i].ReportedAt, reports[rows[i].Instance])
	}
	return out
}

// InventoryCurrent reports whether a collector's beacon evidence says what
// runs now: some instance reported at or after staleBefore, either on one of
// its rows (LastReport) or in its last-report record. The record outlives the
// rows — the sweeper deletes rows by last_seen — so a live instance whose only
// pipeline was dropped and whose rows were swept is still current, and its
// observed set is empty ("nothing running"), not unknown.
func InventoryCurrent(rows []BeaconRow, reports map[string]time.Time, staleBefore time.Time) bool {
	for i := range rows {
		if !LastReport(rows[i]).Before(staleBefore) {
			return true
		}
	}
	for _, at := range reports {
		if !at.Before(staleBefore) {
			return true
		}
	}
	return false
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
