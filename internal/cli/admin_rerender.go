package cli

import (
	"errors"
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

var (
	rerenderDestinationsDryRun bool
	rerenderDestinationsAll    bool
	rerenderDestinationsApply  bool
)

var adminRerenderDestinationsCmd = &cobra.Command{
	Use:   "rerender-destinations",
	Short: "Re-render wizard pipelines from their destinations (legacy sys.env writers, or --all)",
	Long: `rerender-destinations re-runs wizard pipelines from their stored state
against the org's current destinations, validates the result through the same
gate a pipeline edit passes (Stages 1-2 per pipeline, Stage 3 over each org's
merged config), and stores it with a new revision and a pipeline.rerender audit
row. Collectors pick the new config up on their next poll.

Without --all it is the one-time upgrade step for wizard pipelines committed
before destination auth was rendered (#229/#260): their writers read their URL
from sys.env("SHEPHERD_DEST_<NAME>_URL") — a variable nothing sets — and carry
no auth. Run it with --dry-run first.

With --all it regenerates EVERY wizard pipeline whose render differs from what
is stored (#261). Destinations' tenant IDs were stored but not sent before
#261; regenerating starts sending them as X-Scope-OrgID, which moves that data
into the named backend tenant. --all is a dry run unless --apply is given: it
lists, per org, the destinations with a tenant and the pipelines that would
change. A pipeline edited by hand is listed and never overwritten.

A pipeline that cannot be re-rendered (for example, its destination was
deleted) is listed with the reason and what to do — fix what the reason
names (create the destination again, say), or, on the pipeline's page,
detach it from the wizard or delete it — and left unchanged. Safe to run
more than once: a pipeline already re-rendered is not touched again.`,
	RunE: runAdminRerenderDestinations,
}

func init() {
	adminRerenderDestinationsCmd.Flags().BoolVar(&rerenderDestinationsDryRun, "dry-run", false, "list what would be re-rendered without writing anything")
	adminRerenderDestinationsCmd.Flags().BoolVar(&rerenderDestinationsAll, "all", false,
		"regenerate every wizard pipeline whose render differs from what is stored (dry run unless --apply)")
	adminRerenderDestinationsCmd.Flags().BoolVar(&rerenderDestinationsApply, "apply", false, "with --all: write the changes")
	adminCmd.AddCommand(adminRerenderDestinationsCmd)
}

// rerenderDryRun decides whether a run writes: --all is a dry run unless
// --apply is given; without --all only --dry-run makes it one.
func rerenderDryRun(all, apply, dryRun bool) (bool, error) {
	if apply && !all {
		return false, errors.New("--apply only applies to --all; without --all, omit --dry-run to write")
	}
	if apply && dryRun {
		return false, errors.New("--apply and --dry-run contradict each other")
	}
	if all {
		return !apply, nil
	}
	return dryRun, nil
}

func runAdminRerenderDestinations(cmd *cobra.Command, _ []string) error {
	dryRun, err := rerenderDryRun(rerenderDestinationsAll, rerenderDestinationsApply, rerenderDestinationsDryRun)
	if err != nil {
		return err
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
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	if err != nil {
		return fmt.Errorf("loading the Alloy component schema: %w", err)
	}
	out := cmd.OutOrStdout()
	if cfg.Validate.AlloyBinary == "" {
		_, _ = fmt.Fprintln(out, "note: no Alloy binary configured (validate.alloy_binary) — the `alloy validate` part of Stage 2 is skipped") //nolint:errcheck // terminal output
	}
	run := mgmtapi.RerenderLegacyDestinationWriters
	if rerenderDestinationsAll {
		run = mgmtapi.RerenderAllWizardPipelines
	}
	results, err := run(cmd.Context(), st, validate.New(&cfg.Validate), reg, slog.Default(), dryRun)
	failed := printRerenderResults(out, results, dryRun, rerenderDestinationsAll)
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
func printRerenderResults(w io.Writer, results []mgmtapi.LegacyRerenderResult, dryRun, all bool) int {
	verb := "re-rendered"
	if dryRun {
		verb = "would re-render"
	}
	if len(results) == 0 {
		msg := "no wizard pipeline carries the legacy sys.env destination writer — nothing to do"
		if all {
			msg = "every wizard pipeline already matches its destinations — nothing to do"
		}
		_, _ = fmt.Fprintln(w, msg) //nolint:errcheck // terminal output
		return 0
	}
	failed := 0
	for _, r := range results {
		_, _ = fmt.Fprintf(w, "org %s (%s): %s %d pipeline(s)\n", r.OrgName, r.OrgID, verb, len(r.Rerendered)) //nolint:errcheck // terminal output
		for _, t := range r.TenantDestinations {
			_, _ = fmt.Fprintf(w, "  tenant  %s\n", t) //nolint:errcheck // terminal output
		}
		for _, name := range r.Rerendered {
			_, _ = fmt.Fprintf(w, "  ok      %s\n", name) //nolint:errcheck // terminal output
		}
		for _, f := range r.Failed {
			_, _ = fmt.Fprintf(w, "  FAILED  %s\n", f) //nolint:errcheck // terminal output
		}
		failed += len(r.Failed)
	}
	if dryRun && all {
		_, _ = fmt.Fprintln(w, "dry run: nothing written — run again with --apply to regenerate") //nolint:errcheck // terminal output
	}
	return failed
}
