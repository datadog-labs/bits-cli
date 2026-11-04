//go:build unix

package cmd

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
)

// stopDiscardedWait bounds the wait for SIGCONT, since the kernel discards
// SIGTSTP for an orphaned process group.
const stopDiscardedWait = 100 * time.Millisecond

type programSender interface {
	Send(tea.Msg)
}

// watchJobControlStops releases the terminal before the process stops on
// SIGTTIN and restores it on resume. SIGTSTP is left alone: Bubble Tea's
// suspend relies on its default action, which signal.Reset cannot restore.
func watchJobControlStops(ctx context.Context, program programSender) func() {
	// Lets the terminal be released and restored from the background.
	signal.Ignore(syscall.SIGTTOU)

	ctx, cancel := context.WithCancel(ctx)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTTIN)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
			}
			if !suspendProgram(ctx, program) {
				return
			}
			drainSignals(signals)
		}
	}()
	return func() {
		cancel()
		<-done
		signal.Stop(signals)
	}
}

func suspendProgram(ctx context.Context, program programSender) bool {
	resumed := make(chan struct{})
	program.Send(tea.Exec(stopProcess{}, func(error) tea.Msg {
		close(resumed)
		return nil
	})())
	select {
	case <-resumed:
		return true
	case <-ctx.Done():
		return false
	}
}

func drainSignals(signals <-chan os.Signal) {
	for {
		select {
		case <-signals:
		default:
			return
		}
	}
}

// stopProcess stops the process group like ctrl+z and returns once continued.
type stopProcess struct{}

func (stopProcess) Run() error {
	continued := make(chan os.Signal, 1)
	signal.Notify(continued, syscall.SIGCONT)
	defer signal.Stop(continued)
	if err := syscall.Kill(0, syscall.SIGTSTP); err != nil {
		return err
	}
	select {
	case <-continued:
	case <-time.After(stopDiscardedWait):
	}
	return nil
}

func (stopProcess) SetStdin(io.Reader)  {}
func (stopProcess) SetStdout(io.Writer) {}
func (stopProcess) SetStderr(io.Writer) {}
