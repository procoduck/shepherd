package cli

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

var rerenderDestinationsDryRun bool

var adminRerenderDestinationsCmd = &cobra.Command{
	Use:   "rerender-destinations",
	Short: "Re-render wizard pipelines that still carry the legacy sys.env destination writer (one-time upgrade step)",
	Long: `rerender-destinations is the one-time upgrade step for wizard pipelines
committed before destination auth was rendered (#229/#260). Those pipelines'
writers read their URL from sys.env("SHEPHERD_DEST_<NAME>_URL") — a variable
nothing sets — and carry no auth. This command re-runs each one's wizard from
its stored state against the org's current destinations, validates the result
through the same gate a pipeline edit passes (Stages 1-2 per pipeline, Stage 3
over each org's merged config), and stores it with a new revision and a
pipeline.rerender audit row. Collectors pick the new config up on their next
poll.

A pipeline that cannot be re-rendered (for example, its destination was
deleted) is listed and left unchanged. Safe to run more than once: a
re-rendered pipeline no longer matches. Run it with --dry-run first.`,
	RunE: runAdminRerenderDestinations,
}

func init() {
	adminRerenderDestinationsCmd.Flags().BoolVar(&rerenderDestinationsDryRun, "dry-run", false, "list what would be re-rendered without writing anything")
	adminCmd.AddCommand(adminRerenderDestinationsCmd)
}

func runAdminRerenderDestinations(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer st.Close()
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		return fmt.Errorf("loading the Alloy component schema: %w", err)
	}
	out := cmd.OutOrStdout()
	if cfg.Validate.AlloyBinary == "" {
		_, _ = fmt.Fprintln(out, "note: no Alloy binary configured (validate.alloy_binary) — the `alloy validate` part of Stage 2 is skipped") //nolint:errcheck // terminal output
	}
	results, err := mgmtapi.RerenderLegacyDestinationWriters(cmd.Context(), st, validate.New(&cfg.Validate), reg, slog.Default(), rerenderDestinationsDryRun)
	failed := printRerenderResults(out, results, rerenderDestinationsDryRun)
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d pipeline(s) were not re-rendered; fix them and run again", failed)
	}
	return nil
}

// printRerenderResults writes one block per org and returns how many
// pipelines failed.
func printRerenderResults(w io.Writer, results []mgmtapi.LegacyRerenderResult, dryRun bool) int {
	verb := "re-rendered"
	if dryRun {
		verb = "would re-render"
	}
	if len(results) == 0 {
		_, _ = fmt.Fprintln(w, "no wizard pipeline carries the legacy sys.env destination writer — nothing to do") //nolint:errcheck // terminal output
		return 0
	}
	failed := 0
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "org %s (%s): %s %d pipeline(s)\n", r.OrgName, r.OrgID, verb, len(r.Rerendered)) //nolint:errcheck // terminal output
		for _, name := range r.Rerendered {
			_, _ = fmt.Fprintf(w, "  ok      %s\n", name) //nolint:errcheck // terminal output
		}
		for _, f := range r.Failed {
			_, _ = fmt.Fprintf(w, "  FAILED  %s\n", f) //nolint:errcheck // terminal output
		}
		failed += len(r.Failed)
	}
	return failed
}
