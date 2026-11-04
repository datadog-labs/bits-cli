//go:build !unix

package cmd

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

type programSender interface {
	Send(tea.Msg)
}

func watchJobControlStops(context.Context, programSender) func() {
	return func() {}
}
