package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/datadog-labs/bits-cli/internal/update"
)

// newInstallCommand is what the install scripts and Upgrade run from a freshly
// unpacked release. Released scripts and binaries call it with no arguments:
// keep it that way.
func newInstallCommand(invoked *bool, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "install",
		Short:  "Activate this release of a standalone installation",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			*invoked = true
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			// Tests name a scratch key, so they never change the real user PATH.
			environment := os.Getenv("BITS_TEST_USER_ENVIRONMENT")
			if environment == "" {
				environment = "Environment"
			}
			return update.Install(update.InstallOptions{
				Executable:      executable,
				Home:            home,
				Shell:           os.Getenv("SHELL"),
				Path:            os.Getenv("PATH"),
				UserEnvironment: environment,
			}, stderr)
		},
	}
}
