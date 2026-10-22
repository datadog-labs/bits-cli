package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/spf13/cobra"
)

// LoginOptions carries the user-supplied login flags resolved by the command
// tree before dispatch.
type LoginOptions struct {
	Site     string
	ClientID string
}

func newLoginCommand(invoked *bool, stderr io.Writer) *cobra.Command {
	opts := LoginOptions{Site: auth.DefaultSite}
	command := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Datadog",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			*invoked = true
			return runLogin(command.Context(), opts, stderr)
		},
	}
	command.Flags().StringVar(&opts.Site, "site", opts.Site, "Datadog site URL or hostname")
	command.Flags().StringVar(&opts.ClientID, "client-id", opts.ClientID, "OAuth client ID override")
	return command
}

func newLogoutCommand(invoked *bool, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out of Datadog",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			*invoked = true
			return runLogout(command.Context(), stderr)
		},
	}
}

func runLogin(ctx context.Context, opts LoginOptions, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	callbacks := make(chan string)
	readCallbackURLs(ctx, os.Stdin, callbacks)
	session, err := auth.Login(ctx, auth.LoginOptions{
		Site:         opts.Site,
		ClientID:     opts.ClientID,
		CallbackURLs: callbacks,
		OnBrowserOpen: func(_ string, openErr error) {
			if openErr != nil {
				return
			}
			_, _ = fmt.Fprintln(stderr, "\nOpen the sign-in link above in a browser on your computer.\nAfter signing in, if the final page can’t open, copy its full address\nfrom your browser’s address bar and paste it here, then press Enter:")
		},
		Store: auth.DefaultStore(),
		Out:   stderr,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stderr, "Logged in to %s.\n", session.Site)
	return err
}

func runLogout(ctx context.Context, stderr io.Writer) error {
	logoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	hadSession, revokeErr, err := auth.Logout(logoutCtx, auth.DefaultStore(), nil)
	cancel()
	if err != nil {
		return err
	}
	if !hadSession {
		_, err = fmt.Fprintln(stderr, "No Bits CLI OAuth session is stored.")
		return err
	}
	if revokeErr != nil {
		_, err = fmt.Fprintf(stderr, "Logged out locally; remote token revocation failed: %v\n", revokeErr)
		return err
	}
	_, err = fmt.Fprintln(stderr, "Logged out.")
	return err
}

// readCallbackURLs reads lines without changing the terminal's input mode.
// Cancellation stops delivery, but a pending stdin read can remain blocked until
// more input arrives or the standalone login command exits. Stdin stays open.
func readCallbackURLs(ctx context.Context, in io.Reader, urls chan<- string) {
	go func() {
		defer close(urls)
		scanner := bufio.NewScanner(in)
		for ctx.Err() == nil && scanner.Scan() {
			if ctx.Err() != nil {
				return
			}
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			select {
			case urls <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
}
