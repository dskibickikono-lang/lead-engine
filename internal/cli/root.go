// Package cli wires config, store, clients, and stages into the
// lead-engine command tree: `run` and `pipedrive setup`.
package cli

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:           "lead-engine",
	Short:         "Unified B2B lead pipeline: scrape, unify, enrich, deliver",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() error {
	rootCmd.AddCommand(newRunCmd(), newPipedriveCmd())
	return rootCmd.Execute()
}
