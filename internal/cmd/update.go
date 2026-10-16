package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/datadog-labs/bits-cli/internal/config"
	"github.com/datadog-labs/bits-cli/internal/update"
)

func newUpdateCommand(invoked *bool, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update Bits to the latest release",
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load(config.Params{})
			if err != nil {
				return err
			}
			*invoked = true
			return update.New(cfg.Dir).Upgrade(os.Stdin, stdout, stderr)
		},
	}
}
