package grafana

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// ErrNotConfigured is returned by ConnectionStore.Client and
// ConnectionStore.Info when an org has no grafana_connections row.
// "Grafana absent means no outcome verification, never reduced function"
// (plan §5) — every caller of this package is expected to treat this as
// exactly as unexceptional as an empty result, not a system failure, and
// VerifyForOrg does precisely that (folding it into OutcomeUnknown).
var ErrNotConfigured = errors.New("grafana: no connection configured for this org")

// ErrEncryptionUnavailable mirrors internal/mgmtapi's identically-named
// convention (rpc_gitops.go's errEncryptionUnavailable) for the same
// reason: a server booted without an encryption key cannot decrypt (or
// safely encrypt) a stored token, and every write or read through this
// store degrades the same way GitOps credentials already do rather than
// inventing a second contract for "encryption not configured".
var ErrEncryptionUnavailable = errors.New("grafana: encryption not configured on this server")

// ConnectionStore persists and resolves the one Grafana connection an org
// may configure (grafana_connections is UNIQUE(org_id) — see
// 0011_grafana_connections.up.sql). It is the ONLY type in this package
// that ever holds a decrypted token, and it never returns one: Set takes a
// plaintext token as input, Client returns a ready-to-use *Client with the
// token folded into an unexported field, and Info returns everything about
// a connection EXCEPT the token. There is no ConnectionStore method whose
// return type is "the plaintext token" — that is the structural half of
// "never log or return the token" (D7's build brief); do not add one.
type ConnectionStore struct {
	store  *store.Store
	crypto *crypto.Encryptor
}

// NewConnectionStore constructs a ConnectionStore. enc may be nil — every
// method reports ErrEncryptionUnavailable rather than panicking or
// silently storing/serving an unencrypted token, matching
// internal/mgmtapi.GitOpsService's identical nil-encryptor convention.
func NewConnectionStore(st *store.Store, enc *crypto.Encryptor) *ConnectionStore {
	return &ConnectionStore{store: st, crypto: enc}
}

// ConnectionInfo describes a configured connection without its token —
// safe to log, return over an API, or display in a UI, unlike the
// grafana_connections row itself (which carries token_enc).
type ConnectionInfo struct {
	BaseURL   string
	UpdatedAt time.Time
}

// connectionRow is row's return shape: exactly what a caller inside this
// package needs (base URL, still-encrypted token, last update time), never
// exported — Info (below) is the exported view, and it omits TokenEnc
// entirely.
type connectionRow struct {
	BaseURL   string
	TokenEnc  []byte
	UpdatedAt time.Time
}

// row fetches org's grafana_connections row, translating "no such row"
// into ErrNotConfigured (checked via errors.Is(err, pgx.ErrNoRows), this
// repo's established idiom for the same distinction — see
// internal/simulate/worker.go's ClaimQueuedSimulateRun handling) and
// leaving every other database error as-is so a real outage is never
// misreported as "not configured".
func (cs *ConnectionStore) row(ctx context.Context, orgID pgtype.UUID) (connectionRow, error) {
	r, err := cs.store.Queries.GetGrafanaConnectionByOrgID(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connectionRow{}, ErrNotConfigured
		}
		return connectionRow{}, fmt.Errorf("grafana: reading connection: %w", err)
	}
	return connectionRow{BaseURL: r.BaseUrl, TokenEnc: r.TokenEnc, UpdatedAt: r.UpdatedAt.Time}, nil
}

// Info reports whether orgID has a configured connection and, if so, its
// base URL — never its token. Returns ErrNotConfigured (not an error a
// caller needs to treat as unusual) when the org has none.
func (cs *ConnectionStore) Info(ctx context.Context, orgID pgtype.UUID) (ConnectionInfo, error) {
	r, err := cs.row(ctx, orgID)
	if err != nil {
		return ConnectionInfo{}, err
	}
	return ConnectionInfo{BaseURL: r.BaseURL, UpdatedAt: r.UpdatedAt}, nil
}

// Client resolves orgID's connection and returns a ready-to-use *Client,
// with the stored token decrypted in-process for exactly this call and
// folded into the Client's unexported field — never assigned to a local
// variable this function returns or logs. Returns ErrNotConfigured if
// orgID has no connection, ErrEncryptionUnavailable if this store has no
// encryptor, or a wrapped decrypt/parse error for a corrupt row.
func (cs *ConnectionStore) Client(ctx context.Context, orgID pgtype.UUID, timeout time.Duration) (*Client, error) {
	if cs.crypto == nil {
		return nil, ErrEncryptionUnavailable
	}
	r, err := cs.row(ctx, orgID)
	if err != nil {
		return nil, err
	}
	plaintext, err := cs.crypto.Decrypt(r.TokenEnc)
	if err != nil {
		// Never wrap plaintext (there is none to wrap — decryption
		// failed) but also never echo r.TokenEnc's bytes into the error;
		// %w on the crypto package's own error is safe, it never embeds
		// ciphertext (see internal/crypto.Decrypt's error strings).
		return nil, fmt.Errorf("grafana: decrypting stored token for org: %w", err)
	}
	client, err := NewClient(r.BaseURL, string(plaintext), timeout)
	if err != nil {
		return nil, fmt.Errorf("grafana: building client from stored connection: %w", err)
	}
	return client, nil
}

// Set stores (or replaces, via the ON CONFLICT(org_id) upsert —
// 0011_grafana_connections.up.sql) orgID's Grafana connection. token is
// encrypted before it reaches the database and is not retained by this
// function beyond the Encrypt call. Returns ErrEncryptionUnavailable if
// this store has no encryptor — refusing to persist a token Shepherd could
// never subsequently decrypt back out is the loud failure; silently
// storing it in plaintext, or silently declining to store it, both are not.
func (cs *ConnectionStore) Set(ctx context.Context, orgID pgtype.UUID, baseURL, token string) error {
	if cs.crypto == nil {
		return ErrEncryptionUnavailable
	}
	if _, err := validateBaseURL(baseURL); err != nil {
		return fmt.Errorf("grafana: Set: %w", err)
	}
	if token == "" {
		return errors.New("grafana: Set: token is required")
	}
	encToken, err := cs.crypto.Encrypt([]byte(token))
	if err != nil {
		return fmt.Errorf("grafana: encrypting token: %w", err)
	}
	_, err = cs.store.Queries.UpsertGrafanaConnection(ctx, sqlc.UpsertGrafanaConnectionParams{
		OrgID:    orgID,
		BaseUrl:  baseURL,
		TokenEnc: encToken,
	})
	if err != nil {
		return fmt.Errorf("grafana: storing connection: %w", err)
	}
	return nil
}

// Delete removes orgID's connection, if any (returning to Grafana-absent
// behavior for that org — see VerifyForOrg, which then reports
// OutcomeUnknown). Deleting a connection that does not exist is not an
// error, matching DELETE's natural idempotence and this repo's convention
// for the same shape (e.g. internal/mgmtapi.GitOpsService.DeleteCredential).
func (cs *ConnectionStore) Delete(ctx context.Context, orgID pgtype.UUID) error {
	if err := cs.store.Queries.DeleteGrafanaConnection(ctx, orgID); err != nil {
		return fmt.Errorf("grafana: deleting connection: %w", err)
	}
	return nil
}
