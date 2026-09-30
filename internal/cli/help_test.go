package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

// A walkthrough found `shepherd --help` listing migrate, serve and version
// with a blank description. Every command an operator can reach carries a
// one-line summary.
var _ = Describe("shepherd --help", func() {
	It("gives every command a short description", func() {
		var missing []string
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			for _, sub := range c.Commands() {
				if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
					continue
				}
				if sub.Short == "" {
					missing = append(missing, sub.CommandPath())
				}
				walk(sub)
			}
		}
		walk(rootCmd)
		Expect(missing).To(BeEmpty(), "commands with no Short help text")
	})
})
