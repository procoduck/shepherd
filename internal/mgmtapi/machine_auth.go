package mgmtapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"shepherd/internal/store"
)

// onBehalfOfHeader carries G13's second attribution half (see
// requireWriteAuthorized): the human identifier a machine (service
// account) caller's write is performed for. A header rather than a message
// field so every current and future shepherd.mgmt.v1 write path carries it
// uniformly without a proto change per procedure — the same reasoning that
// keeps auth itself (the Authorization header) out of request messages.
const onBehalfOfHeader = "Shepherd-On-Behalf-Of"

// serviceAccountIdentity is a machine caller's identity, populated by
// newServiceAccountAuthInterceptor and consulted by
// requireWriteAuthorized/auditLog (helpers.go). It deliberately carries no
// role — a service account's coarse org access is decided the same way a
// human session's is (authorizeProcedure in rpc_interceptor.go); what is
// unique to a machine caller is Capability (G12) and OnBehalfOf (G13).
type serviceAccountIdentity struct {
	ID         string
	OrgID      string
	Name       string
	Capability string
	// OnBehalfOf is the human this machine write is CLAIMED to be for,
	// read from a request header the caller controls. It is a claim until
	// requireWriteAuthorized checks it against DelegatedPrincipal — never
	// trust it before that.
	OnBehalfOf string
	// DelegatedPrincipal is the human recorded on the service account row
	// itself (service_accounts.created_by), set when the credential was
	// issued by an authenticated admin. The caller cannot influence it,
	// which is what makes it usable as the thing an on-behalf-of claim is
	// checked against.
	DelegatedPrincipal string
}

type machineCtxKey int

const serviceAccountCtxKey machineCtxKey = iota

func withServiceAccount(ctx context.Context, sa serviceAccountIdentity) context.Context {
	return context.WithValue(ctx, serviceAccountCtxKey, sa)
}

// serviceAccountFromCtx returns the authenticated service account for this
// request, if the caller is a machine rather than a human session.
func serviceAccountFromCtx(ctx context.Context) (serviceAccountIdentity, bool) {
	sa, ok := ctx.Value(serviceAccountCtxKey).(serviceAccountIdentity)
	return sa, ok
}

// errBadServiceAccountAuth is returned (without detail) for every
// malformed-or-invalid Basic-auth attempt, mirroring
// internal/agentapi/auth.go's verifyBasicAuth: the failure reason (missing
// header vs bad base64 vs unknown id vs wrong secret) is deliberately not
// distinguished in the response, so a credential-guessing attempt learns
// nothing from which failure mode it hit.
var errBadServiceAccountAuth = errors.New("mgmtapi: invalid service account credentials")

// newServiceAccountAuthInterceptor authenticates an
// `Authorization: Basic <base64(id:secret)>` request as a service account
// (0012_teams_service_accounts), mirroring
// internal/agentapi.NewAuthInterceptor's collector-token shape exactly —
// same id:secret Basic layout, same sha256(secret)+constant-time-compare
// verification.
//
// A request with no Authorization header (or a non-Basic scheme) passes
// through unchanged: that is the human-session path, decided by cookie +
// auth.SessionMiddleware upstream of this interceptor, not here. A request
// that DOES present Basic credentials but they do not verify is rejected
// outright — it never falls through to be treated as an anonymous session,
// which would silently downgrade a bad credential into "no access" instead
// of "who are you" (fail loud, not fail open).
func newServiceAccountAuthInterceptor(st *store.Store) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			authHeader := req.Header().Get("Authorization")
			if !strings.HasPrefix(authHeader, "Basic ") {
				return next(ctx, req) // no Basic credentials presented: human-session path.
			}

			sa, err := verifyServiceAccountBasicAuth(ctx, authHeader, st)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errBadServiceAccountAuth)
			}
			// Verify the on-behalf-of claim HERE, where it enters, rather
			// than only in requireWriteAuthorized. That guard runs on
			// apply-gated writes; propose-safe procedures never call it, and
			// some of them still stamp the claim into an audit row through
			// auditLogDetail. Verifying only at the write gate therefore left
			// exactly one path — SimulateService.CreateRun — able to record an
			// unverified human as the principal, which is the same
			// impersonation this check exists to stop, just on a quieter
			// procedure.
			//
			// A claim that does not match the credential's delegating human is
			// refused outright, on reads as well as writes: there is no benign
			// reading of a caller naming someone it was not issued to.
			if claim := req.Header().Get(onBehalfOfHeader); claim != "" {
				if err := verifyOnBehalfOf(sa, claim); err != nil {
					return nil, err
				}
				sa.OnBehalfOf = claim
			}
			ctx = withServiceAccount(ctx, sa)
			return next(ctx, req)
		}
	}
}

func verifyServiceAccountBasicAuth(ctx context.Context, authHeader string, st *store.Store) (serviceAccountIdentity, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	if err != nil {
		return serviceAccountIdentity{}, errBadServiceAccountAuth
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return serviceAccountIdentity{}, errBadServiceAccountAuth
	}
	idStr, secret := parts[0], parts[1]

	id, err := scanUUID(idStr)
	if err != nil {
		return serviceAccountIdentity{}, errBadServiceAccountAuth
	}
	sa, err := st.Queries.GetServiceAccountByID(ctx, id)
	if err != nil {
		return serviceAccountIdentity{}, errBadServiceAccountAuth
	}
	hash := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(hash[:], sa.TokenHash) != 1 {
		return serviceAccountIdentity{}, errBadServiceAccountAuth
	}
	return serviceAccountIdentity{
		ID: sa.ID.String(), OrgID: sa.OrgID.String(), Name: sa.Name, Capability: sa.Capability,
		DelegatedPrincipal: sa.CreatedBy,
	}, nil
}

// requireWriteAuthorized is called at the top of every mutating
// shepherd.mgmt.v1 handler — per write path (docs/gateway-tier-plan.md
// G12), not from a single shared interceptor. See §10's W1 lesson this
// repo already paid for once: "two paths produce the same served config;
// enforcing one of them is not enforcement." newAuthzInterceptor
// (rpc_interceptor.go) is the only entry Connect traffic has today, but a
// gate that lives ONLY there is one refactor away from a second entry
// (a REST shim, a future direct-call path) silently bypassing it, the same
// shape the W1 gap took. Calling this explicitly, by name, inside each
// handler is what keeps a newly added write path from inheriting
// enforcement it never asked for — see capabilityRequirements'
// enumeration test for the complementary guarantee (every mutating
// procedure IS classified), and this function for the guarantee that
// classification actually does something.
//
// It enforces two independent, orthogonal guards, both no-ops for a human
// session (nil serviceAccountFromCtx) — capability and attribution apply
// only to a machine caller:
//
//  1. G12 capability: a "propose"-capability service account token must
//     never reach an apply (write) path. There is no partial credit —
//     PermissionDenied, not a degraded/dry-run write.
//  2. G13 two-part attribution: a machine caller's write must name the
//     human it acts for (the Shepherd-On-Behalf-Of header), or the write
//     is rejected — Shepherd's chosen answer to "reject or record as
//     unattributed" (docs/gateway-tier-plan.md G13). Reject was chosen
//     over silently recording an unattributed machine write because W10
//     has no legitimate actor yet that mutates production config with no
//     human accountable for it: every current service-account write path
//     is either a CI/automation credential acting for the person who
//     configured the job, or (W11, later) an agent proposing on a
//     specific human's behalf. An audit row that could not name who to
//     ask "why did this change happen" is a worse failure mode than a
//     rejected request the caller can retry with the header set — matches
//     this repo's "guards make silent failures loud" standard applied to
//     the audit trail itself, not just to the mutation.
func requireWriteAuthorized(ctx context.Context) error {
	sa, ok := serviceAccountFromCtx(ctx)
	if !ok {
		return nil // human session: capability/attribution guards do not apply.
	}
	if sa.Capability != capabilityApply {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("service account %q has %q capability; this write requires %q", sa.Name, sa.Capability, capabilityApply))
	}
	if sa.OnBehalfOf == "" {
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("service account %q: %s header is required for a machine write and was not provided", sa.Name, onBehalfOfHeader))
	}
	// Rejecting an ABSENT on-behalf-of and verifying a PRESENT one are two
	// different guarantees, and only the first is about silence. The header
	// is caller-controlled: a credential holder could otherwise name any
	// human — including an org admin who never touched the system — and
	// that name would become the permanent audit record of who authorized
	// the change. Unverified, this field is an impersonation primitive
	// wearing attribution's clothes, which is worse than no attribution at
	// all, because the audit trail reads as authoritative.
	//
	// So the claim is checked against the one identity in this exchange the
	// caller cannot influence: the human recorded on the service-account
	// row when an authenticated admin created it. A machine may act for the
	// person who accepted responsibility for it, and for nobody else.
	//
	// Scope note, deliberately narrow: this means one credential delegates
	// for exactly one human. Shared CI acting for many engineers needs an
	// explicit allow-list on the service account — additive, and a
	// deliberate decision to make rather than a default to fall into.
	// The claim itself was already verified by the auth interceptor, for
	// every procedure rather than only the gated ones — see
	// verifyOnBehalfOf. What remains here is the requirement that a WRITE
	// carry one at all, which reads legitimately do not need.
	return verifyOnBehalfOf(sa, sa.OnBehalfOf)
}

// verifyOnBehalfOf checks a claimed principal against the human recorded on
// the service-account row when an authenticated admin issued the credential.
//
// That recorded human is the only identity in a machine request the caller
// cannot influence, which is what makes it the thing worth checking against.
// The header alone proves nothing: unverified, it lets any credential holder
// name any human — including an org admin who never touched the system — and
// that name becomes the permanent audit record of who authorized the change.
// An unverified claim in a field that reads as authoritative is worse than no
// attribution at all, because the audit trail then lies with confidence.
func verifyOnBehalfOf(sa serviceAccountIdentity, claim string) error {
	if sa.DelegatedPrincipal == "" {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("service account %q records no creating principal, so no on-behalf-of claim about it "+
				"can be verified; re-create the credential so the delegation is anchored to a real human", sa.Name))
	}
	if !strings.EqualFold(strings.TrimSpace(claim), strings.TrimSpace(sa.DelegatedPrincipal)) {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("service account %q may only act on behalf of %q, but the request claimed %q — "+
				"an unverified claim would become the audit record of who authorized this change",
				sa.Name, sa.DelegatedPrincipal, claim))
	}
	return nil
}

// capability values, matching 0012_teams_service_accounts.up.sql's CHECK
// constraint.
const (
	capabilityPropose = "propose"
	capabilityApply   = "apply"
)
