package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	rootCmd = &cobra.Command{Use: "shepherd", Short: "Shepherd — Grafana Alloy fleet manager"}
	v       = viper.New()
)

// Execute runs the root cobra command.
func Execute() error { return rootCmd.Execute() }

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
	rootCmd.PersistentFlags().String("log-level", "", "log level (debug|info|warn|error)")
	rootCmd.PersistentFlags().String("log-format", "", "log format (json|text)")
	_ = v.BindPFlag("log.level", rootCmd.PersistentFlags().Lookup("log-level"))   //nolint:errcheck // static flag binding
	_ = v.BindPFlag("log.format", rootCmd.PersistentFlags().Lookup("log-format")) //nolint:errcheck // static flag binding
}
