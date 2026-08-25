package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Session source values, stored in sessions.source. A session has exactly one,
// and authorization branches on it: SourceLocal resolves roles from org_members,
// SourceOIDC matches IdP groups. They never combine — see authorizeOrgAccess.
const (
	SourceLocal = "local"
	SourceOIDC  = "oidc"
)

// Org role vocabulary for LOCAL users, stored in org_members.role.
//
// Grafana's names, deliberately. RoleOrgEditor is the one that did not exist
// before: access used to be all-or-nothing within an org, so anyone who needed
// to write a pipeline also got the ability to rotate tenant routes and manage
// git credentials.
const (
	OrgRoleAdmin  = "admin"
	OrgRoleEditor = "editor"
	OrgRoleViewer = "viewer"
)

// Sentinels the login path distinguishes. They are separate so the SERVER log
// can say which happened; the response to the caller is deliberately identical
// for all of them (see LocalLoginHandler) — telling an unauthenticated client
// "that user exists but the password is wrong" is a user-enumeration oracle.
var (
	ErrNoSuchUser       = errors.New("auth: no such user")
	ErrWrongPassword    = errors.New("auth: wrong password")
	ErrUserDisabled     = errors.New("auth: user disabled")
	ErrPasswordTooShort = errors.New("auth: password must be at least 8 characters")
)

// minPasswordLength is a floor, not a policy. Deliberately not a
// complexity-rules regime: length is the property that actually correlates
// with strength, and the rest mostly teaches people to write Passw0rd!.
const minPasswordLength = 8

// UserStore is local user management: accounts, passwords and org roles.
//
// It exists alongside the IdP path rather than replacing it. Nothing here
// touches orgs.admin_group_id, orgs.reader_group_id or teams.idp_group_id — an
// OIDC session's access is decided exactly as it was before this file existed.
type UserStore struct {
	store  *store.Store
	logger *slog.Logger
}

// NewUserStore constructs a UserStore.
func NewUserStore(st *store.Store, logger *slog.Logger) *UserStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &UserStore{store: st, logger: logger}
}

// Authenticate verifies a login and password, returning the user on success.
//
// The password comparison runs even when the login does not exist. Skipping it
// would make a missing account measurably faster to reject than a wrong
// password, which is a timing oracle for whether a username is valid — the same
// thing the identical error messages are there to avoid.
func (s *UserStore) Authenticate(ctx context.Context, login, password string) (*sqlc.User, error) {
	user, err := s.store.Queries.GetUserByLogin(ctx, login)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Burn comparable time against a known-bad hash.
			_, _ = VerifyPassword(dummyHash, password) //nolint:errcheck // timing equalisation only
			return nil, ErrNoSuchUser
		}
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	ok, verifyErr := VerifyPassword(user.PasswordHash, password)
	if verifyErr != nil || !ok {
		return nil, ErrWrongPassword
	}
	if user.Disabled {
		return nil, ErrUserDisabled
	}
	if err := s.store.Queries.TouchUserLogin(ctx, user.ID); err != nil {
		// Not fatal: a failed last_login_at write must not deny a valid login.
		s.logger.Warn("recording last login", "err", err, "user_id", user.ID.String())
	}
	return &user, nil
}

// dummyHash is a real argon2id hash of a random value, used only to give the
// no-such-user path the same cost as a wrong-password one.
const dummyHash = "$argon2id$v=19$m=65536,t=1,p=4$YWJjZGVmZ2hpamtsbW5vcA$c2hlcGhlcmQtdGltaW5nLWVxdWFsaXNhdGlvbg"

// OrgRole returns a local user's role in an org, or "" when they are not a
// member.
func (s *UserStore) OrgRole(ctx context.Context, orgID, userID pgtype.UUID) (string, error) {
	role, err := s.store.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: orgID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("looking up org role: %w", err)
	}
	return role, nil
}

// ValidatePassword enforces the length floor.
func ValidatePassword(pw string) error {
	if len(pw) < minPasswordLength {
		return ErrPasswordTooShort
	}
	return nil
}

// BootstrapAdmin creates the first administrator when the users table is empty,
// and does nothing otherwise.
//
// Called once at startup. The empty-table check is what makes it safe to run on
// every boot: it cannot resurrect an account an operator deliberately deleted,
// and it cannot reset the password of an existing one.
//
// The seeded account is created with must_change_password set. A default
// credential that stays valid indefinitely is how "admin/admin" ends up in
// production; forcing the change at first login means the default is only ever
// good for the one action that removes it.
func (s *UserStore) BootstrapAdmin(ctx context.Context) error {
	n, err := s.store.Queries.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("counting users: %w", err)
	}
	if n > 0 {
		return nil
	}

	login := strings.TrimSpace(os.Getenv("SHEPHERD_BOOTSTRAP_ADMIN_LOGIN"))
	if login == "" {
		login = "admin"
	}
	password := os.Getenv("SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD")
	generated := false
	if password == "" {
		password = "admin"
		generated = true
	}
	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("hashing bootstrap password: %w", err)
	}
	u, err := s.store.Queries.CreateUser(ctx, sqlc.CreateUserParams{
		Login:              login,
		Email:              "",
		DisplayName:        "Administrator",
		PasswordHash:       hash,
		IsAppAdmin:         true,
		MustChangePassword: true,
	})
	if err != nil {
		return fmt.Errorf("creating bootstrap admin: %w", err)
	}
	if generated {
		s.logger.Warn("created the first administrator with the DEFAULT password; you must change it at first sign-in",
			"login", login, "password", "admin",
			"hint", "set SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD before first start to avoid this")
	} else {
		s.logger.Info("created the first administrator from SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD; a password change is required at first sign-in",
			"login", login)
	}
	s.logger.Info("bootstrap admin ready", "user_id", u.ID.String())
	return nil
}

// ChangePassword sets a new password for a user, clearing the
// must-change-password gate.
//
// requireCurrent is true for a user changing their OWN password: knowing the
// existing one is what stops a stolen session from silently taking over the
// account. An administrator resetting someone else's password does not know it
// and does not need to — but that path sets mustChange, so the temporary value
// they communicate cannot quietly become permanent.
func (s *UserStore) ChangePassword(ctx context.Context, userID pgtype.UUID, current, next string, requireCurrent bool) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	user, err := s.store.Queries.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("looking up user: %w", err)
	}
	if requireCurrent {
		ok, verifyErr := VerifyPassword(user.PasswordHash, current)
		if verifyErr != nil || !ok {
			return ErrWrongPassword
		}
		if current == next {
			return errors.New("auth: the new password must differ from the current one")
		}
	}
	hash, err := HashPassword(next)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	if err := s.store.Queries.SetUserPassword(ctx, sqlc.SetUserPasswordParams{
		ID: userID, PasswordHash: hash, MustChangePassword: false,
	}); err != nil {
		return fmt.Errorf("saving password: %w", err)
	}
	return nil
}

// MustChangePassword reports whether this session's user still owes a password
// change. False for OIDC sessions, which have no local password to change.
func (s *UserStore) MustChangePassword(ctx context.Context, sess *Session) bool {
	if sess == nil || sess.Source != SourceLocal || !sess.UserID.Valid {
		return false
	}
	user, err := s.store.Queries.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return false
	}
	return user.MustChangePassword
}
