package routeapply

import "time"

// SetClock replaces the reconciler's clock, which only backoff reads.
func SetClock(r *Reconciler, now func() time.Time) { r.now = now }
