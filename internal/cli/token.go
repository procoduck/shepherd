package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"

	"shepherd/internal/signals"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Agent token management (requires DB access)",
	Long: "Agent tokens authenticate collectors. Every token is bound to an organisation,\n" +
		"optionally narrowed to cluster and role allowlists; a token with no organisation\n" +
		"is refused by the agent API until it is bound (`shepherd token bind`).",
}

var (
	tokenName     string
	tokenSecret   string
	tokenID       string // fixed UUID, only with SHEPHERD_DEV_ALLOW_STATIC_TOKEN
	tokenOrg      string
	tokenClusters []string
	tokenRoles    []string
)

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent token bound to an organisation (prints secret once)",
	RunE:  runTokenCreate,
}

var tokenBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Bind an existing agent token to an organisation, in place (the secret is unchanged)",
	Long: "Bind (or rebind) a live agent token to an organisation and replace its cluster/role\n" +
		"allowlists. The secret does not change, so collectors keep their credentials, and\n" +
		"neither does whether the token may claim unclaimed clusters — that is set when the\n" +
		"token is created; create a new token to get a claiming one.",
	RunE: runTokenBind,
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an agent token by ID",
	Args:  cobra.ExactArgs(1),
	RunE:  runTokenRevoke,
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agent tokens (unbound tokens first)",
	RunE:  runTokenList,
}

func init() {
	tokenCreateCmd.Flags().StringVarP(&tokenName, "name", "n", "", "token name (required)")
	if err := tokenCreateCmd.MarkFlagRequired("name"); err != nil {
		panic(err)
	} // programming error if missing
	tokenCreateCmd.Flags().StringVar(&tokenOrg, "org", "", "organisation slug (or id) to bind the token to (required)")
	tokenCreateCmd.Flags().StringSliceVar(&tokenClusters, "clusters", nil, "allowed clusters, comma-separated or repeated (empty = any in the org)")
	tokenCreateCmd.Flags().StringSliceVar(&tokenRoles, "roles", nil, "allowed collector roles, comma-separated or repeated (empty = any)")
	tokenCreateCmd.Flags().StringVar(&tokenSecret, "secret", "", "fixed secret (only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true)")
	tokenCreateCmd.Flags().StringVar(&tokenID, "id", "", "fixed UUID for the token (only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true)")

	tokenBindCmd.Flags().StringVar(&tokenID, "id", "", "token id (required)")
	tokenBindCmd.Flags().StringVar(&tokenOrg, "org", "", "organisation slug (or id) to bind to (required)")
	tokenBindCmd.Flags().StringSliceVar(&tokenClusters, "clusters", nil, "allowed clusters, comma-separated or repeated (empty = any in the org)")
	tokenBindCmd.Flags().StringSliceVar(&tokenRoles, "roles", nil, "allowed collector roles, comma-separated or repeated (empty = any)")
	for _, f := range []string{"id", "org"} {
		if err := tokenBindCmd.MarkFlagRequired(f); err != nil {
			panic(err)
		}
	}

	tokenCmd.AddCommand(tokenCreateCmd, tokenBindCmd, tokenRevokeCmd, tokenListCmd)
	rootCmd.AddCommand(tokenCmd)
}

// validateTokenRoles applies AdminService's rule: a role no collector can
// register with would make the token admit nobody on it.
func validateTokenRoles(roles []string) error {
	for _, role := range roles {
		if role = strings.TrimSpace(role); role != "" {
			if err := signals.ValidateRole(role); err != nil {
				return err
			}
		}
	}
	return nil
}

// lookupTokenOrg resolves --org: an organisation slug, or its id.
func lookupTokenOrg(ctx context.Context, st *store.Store, ref string) (sqlc.Org, error) {
	ref = strings.TrimSpace(ref)
	var id pgtype.UUID
	if id.Scan(ref) == nil {
		org, err := st.Queries.GetOrgByID(ctx, id)
		if err == nil {
			return org, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Org{}, fmt.Errorf("looking up org %q: %w", ref, err)
		}
	}
	org, err := st.Queries.GetOrgByName(ctx, ref)
	if err != nil {
		return sqlc.Org{}, fmt.Errorf("looking up org %q: %w", ref, err)
	}
	return org, nil
}

// tokenAllowlists encodes --clusters/--roles for the jsonb columns.
func tokenAllowlists() (clusters, roles json.RawMessage, err error) {
	if clusters, err = jsonStringArray(tokenClusters); err != nil {
		return nil, nil, fmt.Errorf("encoding clusters: %w", err)
	}
	if roles, err = jsonStringArray(tokenRoles); err != nil {
		return nil, nil, fmt.Errorf("encoding roles: %w", err)
	}
	return clusters, roles, nil
}

func runTokenCreate(cmd *cobra.Command, _ []string) error {
	devMode := os.Getenv("SHEPHERD_DEV_ALLOW_STATIC_TOKEN") == "true"
	// Static secret/id is only allowed in dev/test mode.
	if tokenSecret != "" && !devMode {
		return fmt.Errorf("--secret flag is only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true")
	}
	if tokenID != "" && !devMode {
		return fmt.Errorf("--id flag is only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true")
	}
	// Every token is bound to an org, dev fixtures included: the agent API
	// refuses an unbound token, and Alloy exits when its initial load is
	// refused, so a compose stack creates its org first (`shepherd dev
	// ensure-org`) and mints the token bound to it.
	if strings.TrimSpace(tokenOrg) == "" {
		return fmt.Errorf("--org is required: every agent token is bound to an organisation")
	}
	if err := validateTokenRoles(tokenRoles); err != nil {
		return err
	}

	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	org, err := lookupTokenOrg(cmd.Context(), st, tokenOrg)
	if err != nil {
		return err
	}
	orgID := org.ID
	clusters, roles, err := tokenAllowlists()
	if err != nil {
		return err
	}

	secret := tokenSecret
	if secret == "" {
		// Generate 32 random bytes, base64url-encoded.
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("generating secret: %w", err)
		}
		secret = base64.URLEncoding.EncodeToString(raw)
	}

	hash := sha256.Sum256([]byte(secret))

	var resultID string
	if tokenID != "" {
		var fixedID pgtype.UUID
		if err := fixedID.Scan(tokenID); err != nil {
			return fmt.Errorf("invalid --id UUID: %w", err)
		}
		token, err := st.Queries.CreateAgentTokenWithID(cmd.Context(), sqlc.CreateAgentTokenWithIDParams{
			ID:        fixedID,
			Name:      tokenName,
			TokenHash: hash[:],
			CreatedBy: "cli",
			OrgID:     orgID,
			MayClaim:  true, // the operator CLI has database access: app-admin equivalent
			Clusters:  clusters,
			Roles:     roles,
		})
		if err != nil {
			return fmt.Errorf("creating token: %w", err)
		}
		resultID = token.ID.String()
	} else {
		token, err := st.Queries.CreateAgentToken(cmd.Context(), sqlc.CreateAgentTokenParams{
			Name:      tokenName,
			TokenHash: hash[:],
			CreatedBy: "cli",
			OrgID:     orgID,
			MayClaim:  true, // the operator CLI has database access: app-admin equivalent
			Clusters:  clusters,
			Roles:     roles,
		})
		if err != nil {
			return fmt.Errorf("creating token: %w", err)
		}
		resultID = token.ID.String()
	}

	fmt.Printf("token_id=%s secret=%s\n", resultID, secret)
	fmt.Println("# IMPORTANT: the secret is shown only once. Store it securely.")
	return nil
}

func runTokenBind(cmd *cobra.Command, _ []string) error {
	var id pgtype.UUID
	if err := id.Scan(strings.TrimSpace(tokenID)); err != nil {
		return fmt.Errorf("invalid --id: %w", err)
	}
	if err := validateTokenRoles(tokenRoles); err != nil {
		return err
	}
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	org, err := lookupTokenOrg(cmd.Context(), st, tokenOrg)
	if err != nil {
		return err
	}
	clusters, roles, err := tokenAllowlists()
	if err != nil {
		return err
	}
	tok, err := st.Queries.BindAgentToken(cmd.Context(), sqlc.BindAgentTokenParams{
		ID: id, OrgID: org.ID, Clusters: clusters, Roles: roles,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("no live agent token with id %s", tokenID)
	}
	if err != nil {
		return fmt.Errorf("binding token: %w", err)
	}
	fmt.Printf("token %s bound to org=%s clusters=%s roles=%s\n", tok.ID.String(), org.Name, tok.Clusters, tok.Roles)
	return nil
}

func runTokenRevoke(cmd *cobra.Command, args []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	var id pgtype.UUID
	if err := id.Scan(args[0]); err != nil {
		return fmt.Errorf("invalid token ID: %w", err)
	}

	if _, err := st.Queries.RevokeAgentToken(cmd.Context(), sqlc.RevokeAgentTokenParams{ID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no agent token with id %s", args[0])
		}
		return fmt.Errorf("revoking token: %w", err)
	}
	fmt.Printf("token %s revoked\n", args[0])
	return nil
}

func runTokenList(cmd *cobra.Command, _ []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.Queries.ListAgentTokens(cmd.Context(), pgtype.UUID{})
	if err != nil {
		return fmt.Errorf("listing tokens: %w", err)
	}

	for i := range rows {
		t := &rows[i].AgentToken
		status := "active"
		if t.RevokedAt.Valid {
			status = "revoked"
		}
		org := "UNBOUND"
		if rows[i].OrgName.Valid {
			org = rows[i].OrgName.String
		}
		fmt.Printf("id=%-36s name=%-20s org=%-20s clusters=%s roles=%s status=%s created_by=%s\n",
			t.ID.String(), t.Name, org, t.Clusters, t.Roles, status, t.CreatedBy)
	}
	return nil
}
