package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hrkono/lead-engine/internal/config"
	"github.com/hrkono/lead-engine/internal/deliver"
)

func newPipedriveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pipedrive", Short: "Pipedrive helpers"}
	var cfgPath string
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Create lead-engine custom Organization fields; prints field_keys TOML",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			pd := &deliver.PipedriveClient{BaseURL: cfg.Pipedrive.BaseURL, Token: cfg.Pipedrive.APIToken}
			keys, err := pd.EnsureOrgFields(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "[pipedrive.field_keys]")
			for name, key := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "%s = %q\n", name, key)
			}
			return nil
		},
	}
	setup.Flags().StringVar(&cfgPath, "config", "/etc/lead-engine/config.toml", "config file")
	cmd.AddCommand(setup)
	return cmd
}
