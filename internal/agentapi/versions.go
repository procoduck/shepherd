package agentapi

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/serve"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

const (
	// versionNewWindow rate-limits the re-check a new reported version
	// triggers: at most one per collector per window. An instance chooses
	// its own collector.version, so a flapping one must not buy a check —
	// and an audit row — per poll.
	versionNewWindow = time.Minute
	// versionRecheckWindow is how often a recorded mismatch is re-checked
	// on time alone.
	versionRecheckWindow = 5 * time.Minute
	// versionStaleAfter is how old the recorded fingerprint may get before
	// a check that found nothing new rewrites it anyway, refreshing
	// version_checked_at for the windows above. Below versionRecheckWindow,
	// so a mismatch re-checked on time is re-stamped and not re-run on
	// every poll after.
	versionStaleAfter = time.Minute
)

// checkServedVersions is the serve-time half of #375. Stage 3 refused, at
// save, any item missing from the Alloy version of an instance active then;
// a collector's instances change afterwards (an older pod joins), and the
// config it is served keeps being served — it was validated — but is checked
// again here, where it is handed out, and the outcome recorded on the
// collector (serve.RecordVersionCheck, migration 0044) for the Collectors
// page.
//
// The check runs only when it can find something new (versionCheckDue). A
// steady fleet's poll costs nothing beyond reading the collector row it
// already upserted. Best effort: a failure is logged, never the poll's
// error.
func (s *Service) checkServedVersions(ctx context.Context, orgID pgtype.UUID, coll sqlc.Collector, reported, content, hash string) {
	now := time.Now()
	if !versionCheckDue(coll, reported, hash, s.versionRecheck, s.versionNewWindow, now) {
		return
	}
	rows, err := s.store.Queries.ListActiveInstanceVersions(ctx, sqlc.ListActiveInstanceVersionsParams{
		CollectorID:    coll.ID,
		InactiveBefore: inactiveBefore(s.inactiveAfter, now),
	})
	if err != nil {
		s.logger.Warn("version check: listing instance versions", "collector_id", coll.ID.String(), "err", err)
		return
	}
	versions := make([]string, 0, len(rows))
	var until pgtype.Timestamptz
	for _, r := range rows {
		versions = append(versions, r.AlloyVersion)
		// A version drops out once its newest instance ages out; the first
		// to do so is when a re-check can first change the outcome.
		if s.inactiveAfter > 0 && r.NewestSeen.Valid {
			t := r.NewestSeen.Time.Add(s.inactiveAfter)
			if !until.Valid || t.Before(until.Time) {
				until = pgtype.Timestamptz{Time: t, Valid: true}
			}
		}
	}
	findings, err := serve.RecordVersionCheck(ctx, s.store, serve.VersionCheck{
		CollectorID: coll.ID, OrgID: orgID, Content: content, Hash: hash, Versions: versions,
		Until: until, StaleAfter: versionStaleAfter,
	})
	if err != nil {
		s.logger.Warn("version check: recording", "collector_id", coll.ID.String(), "err", err)
		return
	}
	if len(findings) > 0 {
		s.logger.Debug("version check: served config has findings", "collector_id", coll.ID.String(),
			"versions", len(versions), "findings", len(findings))
	}
}

// versionCheckDue reports whether a poll must re-run the version check:
//
//   - the served hash differs from the one last checked (Shepherd's own
//     change, never an agent's);
//   - the polling instance reports a version the last check did not see
//     (compared as validate.RecordedVersion records it), at most once per
//     newWindow;
//   - a recorded mismatch is older than recheck;
//   - a recorded warning's version_checked_until has passed: a checked
//     version can have aged out, which may clear it.
//
// A collector with no warning is never re-checked on time alone: an instance
// going inactive can only remove findings.
func versionCheckDue(coll sqlc.Collector, reported, hash string, recheck, newWindow time.Duration, now time.Time) bool {
	checkedAge := time.Duration(1<<63 - 1)
	if coll.VersionCheckedAt.Valid {
		checkedAge = now.Sub(coll.VersionCheckedAt.Time)
	}
	switch {
	case !coll.VersionCheckedHash.Valid || coll.VersionCheckedHash.String != hash:
		return true
	case !slices.Contains(coll.VersionCheckedVersions, validate.RecordedVersion(reported)):
		return checkedAge >= newWindow
	case !coll.VersionWarning.Valid:
		return false
	case coll.VersionMismatch && checkedAge >= recheck:
		return true
	default:
		return coll.VersionCheckedUntil.Valid && !now.Before(coll.VersionCheckedUntil.Time)
	}
}
