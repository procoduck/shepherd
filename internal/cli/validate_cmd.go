package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/validate"
)

var validateCmd = &cobra.Command{
	Use:   "validate <file.alloy>",
	Short: "Validate an Alloy config file (stages 1–2)",
	Args:  cobra.ExactArgs(1),
	RunE:  runValidate,
}

func init() {
	rootCmd.AddCommand(validateCmd)
}

func runValidate(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	data, err := os.ReadFile(filePath) //nolint:gosec // G304: intentional file path from CLI argument
	if err != nil {
		return fmt.Errorf("reading file %q: %w", filePath, err)
	}
	content := string(data)

	// Stage 1: syntax check.
	r1 := validate.Stage1(content)
	if !r1.Valid {
		for _, d := range r1.Diagnostics {
			fmt.Fprintf(os.Stderr, "%s:%d:%d: %s\n", filePath, d.Line, d.Col, d.Message)
		}
		return fmt.Errorf("validation failed (stage 1: syntax)")
	}

	// Stage 2: semantic check (requires alloy binary).
	valCfg := &config.ValidateConfig{
		AlloyBinary:    "/usr/local/bin/alloy",
		StabilityLevel: "experimental",
		Timeout:        10e9, // 10s in nanoseconds
	}
	// Override from config file if available.
	if cfg, err := config.Load(cfgFile); err == nil {
		valCfg = &cfg.Validate
	}

	v := validate.New(valCfg)
	r2 := v.Stage2(cmd.Context(), content)
	if !r2.Valid {
		for _, d := range r2.Diagnostics {
			fmt.Fprintf(os.Stderr, "%s:%d:%d: %s\n", filePath, d.Line, d.Col, d.Message)
		}
		return fmt.Errorf("validation failed (stage 2: semantic)")
	}

	fmt.Printf("%s: OK\n", filePath)
	return nil
}
