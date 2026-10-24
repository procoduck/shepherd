package mgmtapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"shepherd/internal/auth"
)

// TestMapError pins mapError's sentinel -> connect.Code table. It is a
// plain *testing.T table test (no Docker, no Ginkgo Label) because mapError
// is a pure function with no store/network dependency — see
// docs/archive/api-contract-design.md's testing rules on matching test
// weight to what a spec actually exercises.
func TestMapError(t *testing.T) {
	uniqueViolation := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
	fkViolation := &pgconn.PgError{Code: "23503", Message: "violates foreign key constraint"}
	otherPgError := &pgconn.PgError{Code: "57014", Message: "canceling statement due to user request"}
	plainErr := errors.New("boom")

	cases := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"nil", nil, connect.Code(0) /* unused: nil is handled separately below */},
		{"pgx.ErrNoRows", pgx.ErrNoRows, connect.CodeNotFound},
		{"wrapped pgx.ErrNoRows", errors.Join(errors.New("querying x"), pgx.ErrNoRows), connect.CodeNotFound},
		{"auth.ErrUnauthenticated", auth.ErrUnauthenticated, connect.CodeUnauthenticated},
		{"auth.ErrOrgNotFound", auth.ErrOrgNotFound, connect.CodeNotFound},
		{"auth.ErrInvalidOrgID", auth.ErrInvalidOrgID, connect.CodeInvalidArgument},
		{"auth.ErrHelmManaged", auth.ErrHelmManaged, connect.CodeFailedPrecondition},
		{"auth.ErrEncryptionUnavailable", auth.ErrEncryptionUnavailable, connect.CodeFailedPrecondition},
		{"auth.ErrNoSettings", auth.ErrNoSettings, connect.CodeFailedPrecondition},
		{"pgconn unique violation (23505)", uniqueViolation, connect.CodeAlreadyExists},
		{"pgconn FK violation (23503)", fkViolation, connect.CodeAlreadyExists},
		{"an unrecognized pgconn error", otherPgError, connect.CodeInternal},
		{"a schema mismatch mid-rollout (42P10)", &pgconn.PgError{Code: "42P10"}, connect.CodeUnavailable},
		{"an undefined column mid-rollout (42703)", &pgconn.PgError{Code: "42703"}, connect.CodeUnavailable},
		{"an undefined table mid-rollout (42P01)", &pgconn.PgError{Code: "42P01"}, connect.CodeUnavailable},
		{"an unrecognized plain error", plainErr, connect.CodeInternal},
	}

	if got := mapError(nil); got != nil {
		t.Fatalf("mapError(nil) = %v, want nil", got)
	}

	for _, c := range cases {
		if c.name == "nil" {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			got := mapError(c.err)
			if code := connect.CodeOf(got); code != c.want {
				t.Fatalf("mapError(%v): CodeOf = %v, want %v", c.err, code, c.want)
			}
		})
	}

	// 0.22 -> 0.23 upgrade walkthrough: no Postgres text (constraint names,
	// SQLSTATEs) reaches a client through mapError, whatever the code.
	t.Run("no Postgres error text is leaked to the client", func(t *testing.T) {
		for _, pgErr := range []*pgconn.PgError{
			{Code: "23505", Message: `duplicate key value violates unique constraint "orgs_name_key"`},
			{Code: "23503", Message: `insert or update on table "x" violates foreign key constraint "x_fkey"`},
			{Code: "42P10", Message: "there is no unique or exclusion constraint matching the ON CONFLICT specification"},
		} {
			got := mapError(fmt.Errorf("upserting instance: %w", pgErr)).Error()
			if strings.Contains(got, "SQLSTATE") || strings.Contains(got, "constraint") || strings.Contains(got, "upserting") {
				t.Fatalf("mapError(%s) leaked the store error: %q", pgErr.Code, got)
			}
		}
	})

	t.Run("unrecognized error message is not leaked to the client", func(t *testing.T) {
		got := mapError(errors.New("dsn=postgres://shepherd:s3cr3t@db/shepherd sensitive detail"))
		if connect.CodeOf(got) != connect.CodeInternal {
			t.Fatalf("expected CodeInternal, got %v", connect.CodeOf(got))
		}
		if got.Error() != "internal: internal error" {
			t.Fatalf("mapError must not leak the raw error to the client, got %q", got.Error())
		}
	})
}
