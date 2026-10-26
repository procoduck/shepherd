package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// AuditActionVersionWarning is the audit_log action RecordVersionCheck
// writes when a collector's version warning changes (set, changed, cleared).
const AuditActionVersionWarning = "collector.version_warning"

// maxVersionFindings caps the findings recorded on one collector; the rest
// are summarised in one line.
const maxVersionFindings = 20

// VersionCheck is one served config to check against the Alloy versions a
// collector's active instances report (#375).
type VersionCheck struct {
	CollectorID pgtype.UUID
	OrgID       pgtype.UUID
	// Content and Hash are the config being served and its hash.
	Content string
	Hash    string
	// Versions are the active instances' collector.version values
	// (sqlc ListActiveInstanceVersions); '' for an instance that reported
	// none.
	Versions []string
	// Until is when the first of Versions can drop out of the active set
	// (its newest instance's last_seen plus agent.inactive_after); invalid
	// when none can. Recorded only while a warning stands: it is when a
	// re-check could clear it.
	Until pgtype.Timestamptz
	// StaleAfter is how old a recorded fingerprint may get before an
	// unchanged check rewrites it (refreshing version_checked_at).
	StaleAfter time.Duration
}

// VersionFindings runs validate.CheckAlloyVersions over content and returns
// its findings as the collector records them — missing items first, each
// "line N: message" — and whether any is a missing item (mismatch).
func VersionFindings(content string, versions []string) (findings []string, mismatch bool) {
	diags := validate.CheckAlloyVersions(content, versions)
	var notes []string
	for _, d := range diags {
		msg := d.Message
		if d.Line > 0 {
			msg = fmt.Sprintf("line %d: %s", d.Line, msg)
		}
		if d.Warning {
			notes = append(notes, msg)
			continue
		}
		mismatch = true
		findings = append(findings, msg)
	}
	findings = append(findings, notes...)
	if len(findings) > maxVersionFindings {
		rest := len(findings) - maxVersionFindings
		findings = append(findings[:maxVersionFindings], fmt.Sprintf("and %d more", rest))
	}
	return findings, mismatch
}

// RecordVersionCheck checks a served config against its collector's active
// Alloy versions and records the outcome on the collector row (migration
// 0044). It never changes what is served: the config passed every
// validation stage when it was saved, and a collector whose instances
// changed since then (an older pod joined) keeps being served it, flagged.
//
// The warning is written with a compare-and-swap (SetCollectorVersionWarning)
// and audited only when that write lands, so a warning is audited once per
// change however many pods poll; the check's fingerprint (served hash and
// versions) is recorded every time so the caller can skip the next one.
func RecordVersionCheck(ctx context.Context, st *store.Store, in VersionCheck) (findings []string, err error) {
	findings, mismatch := VersionFindings(in.Content, in.Versions)
	warning := pgtype.Text{String: strings.Join(findings, "\n"), Valid: len(findings) > 0}

	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return findings, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	q := st.Queries.WithTx(tx)

	_, setErr := q.SetCollectorVersionWarning(ctx, sqlc.SetCollectorVersionWarningParams{
		ID: in.CollectorID, Warning: warning, Mismatch: mismatch,
	})
	switch {
	case setErr == nil:
		detail, mErr := json.Marshal(map[string]any{
			"findings":    findings,
			"mismatch":    mismatch,
			"versions":    in.Versions,
			"served_hash": in.Hash,
			"cleared":     !warning.Valid,
		})
		if mErr != nil {
			return findings, fmt.Errorf("encoding audit detail: %w", mErr)
		}
		if err := q.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor:        "serve",
			ActorType:    "system",
			OrgID:        in.OrgID,
			Action:       AuditActionVersionWarning,
			ResourceType: "collector",
			ResourceID:   in.CollectorID.String(),
			Detail:       detail,
		}); err != nil {
			return findings, fmt.Errorf("writing audit row: %w", err)
		}
	case errors.Is(setErr, pgx.ErrNoRows):
		// The stored warning already says this.
	default:
		return findings, fmt.Errorf("recording version warning: %w", setErr)
	}

	versions := in.Versions
	if versions == nil {
		versions = []string{}
	}
	var until pgtype.Timestamptz
	if warning.Valid {
		until = in.Until
	}
	if err := q.RecordCollectorVersionCheck(ctx, sqlc.RecordCollectorVersionCheckParams{
		ID: in.CollectorID, Hash: in.Hash, Versions: versions, Until: until,
		StaleAfterSecs: in.StaleAfter.Seconds(),
	}); err != nil {
		return findings, fmt.Errorf("recording version check: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return findings, fmt.Errorf("committing: %w", err)
	}
	return findings, nil
}
