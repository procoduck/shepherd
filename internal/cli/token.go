package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Agent token management (requires DB access)",
}

var (
	tokenName   string
	tokenSecret string
	tokenID     string // fixed UUID, only with SHEPHERD_DEV_ALLOW_STATIC_TOKEN
)

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new agent token (prints secret once)",
	RunE:  runTokenCreate,
}

var tokenRevokeCmd = &cobra.Command{
	Use:   "revoke <id>",
	Short: "Revoke an agent token by ID",
	Args:  cobra.ExactArgs(1),
	RunE:  runTokenRevoke,
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agent tokens",
	RunE:  runTokenList,
}

func init() {
	tokenCreateCmd.Flags().StringVarP(&tokenName, "name", "n", "", "token name (required)")
	if err := tokenCreateCmd.MarkFlagRequired("name"); err != nil {
		panic(err)
	} // programming error if missing
	tokenCreateCmd.Flags().StringVar(&tokenSecret, "secret", "", "fixed secret (only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true)")
	tokenCreateCmd.Flags().StringVar(&tokenID, "id", "", "fixed UUID for the token (only allowed when SHEPHERD_DEV_ALLOW_STATIC_TOKEN=true)")

	tokenCmd.AddCommand(tokenCreateCmd, tokenRevokeCmd, tokenListCmd)
	rootCmd.AddCommand(tokenCmd)
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

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}

	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer st.Close()

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

func runTokenRevoke(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()

	var id pgtype.UUID
	if err := id.Scan(args[0]); err != nil {
		return fmt.Errorf("invalid token ID: %w", err)
	}

	if err := st.Queries.RevokeAgentToken(cmd.Context(), id); err != nil {
		return fmt.Errorf("revoking token: %w", err)
	}
	fmt.Printf("token %s revoked\n", args[0])
	return nil
}

func runTokenList(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()

	tokens, err := st.Queries.ListAgentTokens(cmd.Context())
	if err != nil {
		return fmt.Errorf("listing tokens: %w", err)
	}

	for i := range tokens {
		t := &tokens[i]
		idStr := t.ID.String()
		status := "active"
		if t.RevokedAt.Valid {
			status = "revoked"
		}
		fmt.Printf("id=%-36s name=%-20s status=%s created_by=%s\n", idStr, t.Name, status, t.CreatedBy)
	}
	return nil
}
