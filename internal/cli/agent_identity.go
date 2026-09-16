package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// agent-identity manages the bindings that map a collector's OIDC identity
// (issuer + app id) to an organisation — resolution mode 1 of
// docs/plans/2026-09-16-agent-oidc-auth.md. It is the operator surface for
// collector OIDC until the admin UI lands, and mirrors `shepherd token`.

var agentIdentityCmd = &cobra.Command{
	Use:   "agent-identity",
	Short: "Collector OIDC identity bindings (requires DB access)",
	Long: "Map a collector's OIDC identity (issuer + app id) to an organisation, so a\n" +
		"collector authenticating with an access token is served that org's config.",
}

var (
	aiIssuer   string
	aiAppID    string
	aiOrg      string
	aiClusters []string
	aiRoles    []string
)

var agentIdentityCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Bind an OIDC identity to an organisation",
	RunE:  runAgentIdentityCreate,
}

var agentIdentityListCmd = &cobra.Command{
	Use:   "list",
	Short: "List collector OIDC identity bindings",
	RunE:  runAgentIdentityList,
}

var agentIdentityDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Remove a binding by issuer and app id",
	RunE:  runAgentIdentityDelete,
}

func init() {
	agentIdentityCreateCmd.Flags().StringVar(&aiIssuer, "issuer", "", "token issuer (required, exact `iss`)")
	agentIdentityCreateCmd.Flags().StringVar(&aiAppID, "app-id", "", "app identity claim value — sub/azp/client_id (required)")
	agentIdentityCreateCmd.Flags().StringVar(&aiOrg, "org", "", "organisation slug to bind to (required)")
	agentIdentityCreateCmd.Flags().StringSliceVar(&aiClusters, "cluster", nil, "allowed cluster (repeatable; empty = any in the org)")
	agentIdentityCreateCmd.Flags().StringSliceVar(&aiRoles, "role", nil, "allowed collector role (repeatable; empty = any)")
	for _, f := range []string{"issuer", "app-id", "org"} {
		if err := agentIdentityCreateCmd.MarkFlagRequired(f); err != nil {
			panic(err) // programming error
		}
	}

	agentIdentityDeleteCmd.Flags().StringVar(&aiIssuer, "issuer", "", "token issuer (required)")
	agentIdentityDeleteCmd.Flags().StringVar(&aiAppID, "app-id", "", "app identity claim value (required)")
	for _, f := range []string{"issuer", "app-id"} {
		if err := agentIdentityDeleteCmd.MarkFlagRequired(f); err != nil {
			panic(err)
		}
	}

	agentIdentityCmd.AddCommand(agentIdentityCreateCmd, agentIdentityListCmd, agentIdentityDeleteCmd)
	rootCmd.AddCommand(agentIdentityCmd)
}

func openStore(cmd *cobra.Command) (*store.Store, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return st, nil
}

// jsonStringArray marshals an allowlist to the jsonb the column expects. A nil
// or empty slice becomes "[]" — "any within the org".
func jsonStringArray(in []string) (json.RawMessage, error) {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func runAgentIdentityCreate(cmd *cobra.Command, _ []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	org, err := st.Queries.GetOrgByName(cmd.Context(), aiOrg)
	if err != nil {
		return fmt.Errorf("looking up org %q: %w", aiOrg, err)
	}
	clusters, err := jsonStringArray(aiClusters)
	if err != nil {
		return fmt.Errorf("encoding clusters: %w", err)
	}
	roles, err := jsonStringArray(aiRoles)
	if err != nil {
		return fmt.Errorf("encoding roles: %w", err)
	}
	if _, err := st.Queries.CreateAgentIdentity(cmd.Context(), sqlc.CreateAgentIdentityParams{
		Issuer:    strings.TrimSpace(aiIssuer),
		AppID:     strings.TrimSpace(aiAppID),
		OrgID:     org.ID,
		Clusters:  clusters,
		Roles:     roles,
		CreatedBy: "cli",
	}); err != nil {
		return fmt.Errorf("creating binding: %w", err)
	}
	fmt.Printf("bound issuer=%s app_id=%s to org=%s\n", aiIssuer, aiAppID, aiOrg)
	return nil
}

func runAgentIdentityList(cmd *cobra.Command, _ []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.Queries.ListAgentIdentities(cmd.Context())
	if err != nil {
		return fmt.Errorf("listing bindings: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("no collector OIDC identity bindings")
		return nil
	}
	fmt.Printf("%-40s  %-30s  %-20s  %s\n", "ISSUER", "APP_ID", "ORG", "CLUSTERS/ROLES")
	for _, r := range rows {
		fmt.Printf("%-40s  %-30s  %-20s  %s / %s\n",
			r.Issuer, r.AppID, r.OrgName, string(r.Clusters), string(r.Roles))
	}
	return nil
}

func runAgentIdentityDelete(cmd *cobra.Command, _ []string) error {
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	n, err := st.Queries.DeleteAgentIdentity(cmd.Context(), sqlc.DeleteAgentIdentityParams{
		Issuer: strings.TrimSpace(aiIssuer),
		AppID:  strings.TrimSpace(aiAppID),
	})
	if err != nil {
		return fmt.Errorf("deleting binding: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no binding for issuer=%s app_id=%s", aiIssuer, aiAppID)
	}
	fmt.Printf("removed binding issuer=%s app_id=%s\n", aiIssuer, aiAppID)
	return nil
}
