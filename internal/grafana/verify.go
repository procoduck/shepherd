package grafana

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Outcome is one of exactly three answers to "did the data arrive?". The
// build brief this package implements is explicit that THREE outcomes are
// load-bearing, not two: "Collapsing the third into 'did not arrive' would
// report a broken Grafana as a broken pipeline." That distinction is made
// structural here, not just documented, by giving OutcomeUnknown the zero
// value: any VerificationResult a bug leaves half-constructed — an
// unreachable code path, a forgotten field assignment, a zero-valued
// struct literal in a test — reads as "could not determine" rather than
// silently reading as OutcomeNotArrived (which it would if this were a
// bool, and OutcomeArrived (which it would if OutcomeArrived were 0
// instead). TestOutcomeZeroValueIsUnknown pins this.
type Outcome int

const (
	// OutcomeUnknown means Shepherd could not determine whether data
	// arrived: no Grafana connection is configured, Grafana was
	// unreachable or timed out, or the query itself failed. See this
	// type's doc comment for why this is the zero value.
	OutcomeUnknown Outcome = iota
	// OutcomeArrived means the query reached Grafana and at least one
	// data point came back.
	OutcomeArrived
	// OutcomeNotArrived means the query reached Grafana, succeeded, and
	// returned zero data points — a definitive negative, not an inability
	// to check.
	OutcomeNotArrived
)

func (o Outcome) String() string {
	switch o {
	case OutcomeArrived:
		return "arrived"
	case OutcomeNotArrived:
		return "not_arrived"
	case OutcomeUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("grafana.Outcome(%d)", int(o))
	}
}

// VerificationResult is Verify's only return value — see Verify's doc
// comment for why it has no accompanying error.
type VerificationResult struct {
	Outcome Outcome
	// Reason is always populated (even for OutcomeArrived, e.g. "1 data
	// point returned"), so a caller displaying this result never has to
	// special-case which outcomes explain themselves.
	Reason    string
	CheckedAt time.Time
}

// unknown is the one place every "could not determine" VerificationResult
// is built, so every OutcomeUnknown result carries the same CheckedAt
// convention (time.Now(), called exactly once here) rather than each call
// site remembering to set it.
func unknown(reasonFormat string, args ...any) VerificationResult {
	return VerificationResult{
		Outcome:   OutcomeUnknown,
		Reason:    fmt.Sprintf(reasonFormat, args...),
		CheckedAt: time.Now(),
	}
}

// Verify queries client for req and reports the outcome. It has NO error
// return: every failure mode (bad request, unreachable Grafana, query
// error, timeout) is folded into OutcomeUnknown with an explanatory
// Reason, rather than an error a caller's own error-handling could
// propagate into failing whatever triggered the verification (a pipeline
// write, per the build brief: "must ... never hang a request or fail a
// pipeline write"). A function that returned (VerificationResult, error)
// would rely on every future caller remembering "a non-nil err here still
// isn't fatal" — dropping the error return removes that chance to forget.
func Verify(ctx context.Context, client *Client, req QueryDatasourceRequest) VerificationResult {
	if client == nil {
		return unknown("grafana: no client configured")
	}
	hasData, err := client.QueryDatasource(ctx, req)
	if err != nil {
		return unknown("grafana: query failed: %v", err)
	}
	if hasData {
		return VerificationResult{Outcome: OutcomeArrived, Reason: "query returned at least one data point", CheckedAt: time.Now()}
	}
	return VerificationResult{Outcome: OutcomeNotArrived, Reason: "query succeeded and returned zero data points", CheckedAt: time.Now()}
}

// VerifyForOrg is the entry point a caller with only an org id (not
// already an *Client) uses: resolve org's Grafana connection through cs,
// then Verify against it. Every way ConnectionStore.Client can fail —
// encryption not configured, no connection configured for this org, a
// genuine database error, a corrupt stored token — degrades to
// OutcomeUnknown here, same as every failure inside Verify itself. This is
// connection_test.go's "Grafana absent (no configured connection)" specs
// exercise, against a real database with no row in grafana_connections at
// all.
func VerifyForOrg(ctx context.Context, cs *ConnectionStore, orgID pgtype.UUID, req QueryDatasourceRequest, timeout time.Duration) VerificationResult {
	client, err := cs.Client(ctx, orgID, timeout)
	if err != nil {
		if errors.Is(err, ErrNotConfigured) {
			return unknown("grafana: no Grafana connection configured for this org")
		}
		if errors.Is(err, ErrEncryptionUnavailable) {
			return unknown("grafana: encryption not configured on this server, cannot decrypt stored Grafana token")
		}
		return unknown("grafana: resolving Grafana connection: %v", err)
	}
	return Verify(ctx, client, req)
}
