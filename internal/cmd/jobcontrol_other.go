//go:build !unix

package cmd

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

type programSender interface {
	Send(tea.Msg)
}

// watchJobControlStops is a no-op where POSIX job control does not exist.
func watchJobControlStops(context.Context, programSender) func() {
	return func() {}
}
