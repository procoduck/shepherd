package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
	// none. They are agent-chosen: RecordVersionCheck checks and records
	// them as validate.RecordedVersions does (each bounded, distinct, at most
	// validate.MaxReportedVersions, the rest counted).
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

// findingLinePrefix is the "line N: " VersionFindings puts before a finding
// with a position in the served config.
var findingLinePrefix = regexp.MustCompile(`(?m)^line [0-9]+: `)

// versionWarningIdentity is what makes two recorded warnings the same for the
// audit: the findings without their served-config line numbers. Any change
// above a finding (another pipeline enabled, disabled or edited, the header's
// revision list) moves it, and a moved finding is not a new one (0.21
// walkthrough: a disable moved one from line 487 to 486 and audited it again).
// The messages themselves carry no position.
//
// Nor are the reported versions a resolution note quotes ("runs Alloy
// "nightly-7", which is not a release version; checked against ...") part of
// it: a note says how a class of version was checked, and an instance whose
// unknown version changes on every poll is in the same state each time (v0.20.0
// follow-up: it bought an audit row per re-check). The note's kind and schema
// stay, so a version moving to another kind of resolution, or an item
// appearing or clearing, is still a change.
func versionWarningIdentity(warning pgtype.Text) pgtype.Text {
	if !warning.Valid {
		return warning
	}
	s := findingLinePrefix.ReplaceAllString(warning.String, "")
	s = noteSamples.ReplaceAllString(s, "${1}…${2}")
	return pgtype.Text{String: s, Valid: true}
}

// noteSamples matches the quoted versions of a resolution note
// (validate.resolutionNote: "runs Alloy <versions>, <how>"). The versions are
// sanitised — no spaces or commas inside one — so ", which ", ", older than "
// and ", newer than " can only end the list.
var noteSamples = regexp.MustCompile(`(?m)^(runs Alloy ).+?(, (?:which |older than |newer than ))`)

// RecordVersionCheck checks a served config against its collector's active
// Alloy versions and records the outcome on the collector row (migration
// 0044). It never changes what is served: the config passed every
// validation stage when it was saved, and a collector whose instances
// changed since then (an older pod joined) keeps being served it, flagged.
//
// The warning is written with a compare-and-swap (SetCollectorVersionWarning)
// under a row lock, and audited only when that write lands and changes what
// the warning says (versionWarningIdentity: line numbers aside), so a warning
// is audited once per change however many pods poll and however its lines
// move; the stored text still follows the lines. The check's fingerprint
// (served hash and versions) is recorded every time so the caller can skip
// the next one.
func RecordVersionCheck(ctx context.Context, st *store.Store, in VersionCheck) (findings []string, err error) {
	// Agent-chosen versions reach the fingerprint and the audit row only
	// bounded: each cut to validate.MaxRecordedVersionLen, at most
	// validate.MaxReportedVersions of them, and the check itself told how
	// many were cut (so they are still checked against the oldest schema).
	kept, total := validate.RecordedVersions(in.Versions)
	findings, mismatch := VersionFindings(in.Content, validate.WithTruncatedVersions(kept, total-len(kept)))
	warning := pgtype.Text{String: strings.Join(findings, "\n"), Valid: len(findings) > 0}

	tx, err := st.Pool().Begin(ctx)
	if err != nil {
		return findings, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	q := st.Queries.WithTx(tx)

	// No row: the collector is gone, and the update below finds nothing too.
	previous, err := q.LockCollectorVersionWarning(ctx, in.CollectorID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return findings, fmt.Errorf("reading version warning: %w", err)
	}
	moved := warning.Valid && previous.Valid && versionWarningIdentity(previous) == versionWarningIdentity(warning)
	_, setErr := q.SetCollectorVersionWarning(ctx, sqlc.SetCollectorVersionWarningParams{
		ID: in.CollectorID, Warning: warning, Mismatch: mismatch, KeepSince: moved,
	})
	switch {
	case setErr == nil && moved:
		// Only line numbers moved: the text follows them, the audit and the
		// warning's "since" do not.
	case setErr == nil:
		detail, mErr := json.Marshal(map[string]any{
			"findings":       findings,
			"mismatch":       mismatch,
			"versions":       kept,
			"versions_total": total,
			"served_hash":    in.Hash,
			"cleared":        !warning.Valid,
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

	versions := kept
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
