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

// stopDiscardedWait bounds how long a suspension waits for SIGCONT. The
// kernel discards SIGTSTP for an orphaned process group, so the TUI would
// otherwise wait forever with the terminal released. A process that did stop
// only observes the timer after it is continued.
const stopDiscardedWait = 100 * time.Millisecond

// The TUI keeps the terminal in raw mode, on the alternate screen and with
// mouse reporting enabled. If the kernel stops it with SIGTTIN, because it
// read the terminal from a background process group, the shell gets back a
// terminal that echoes every mouse movement as text.
//
// SIGTSTP is deliberately not caught. Bubble Tea's own suspend raises it to
// stop the process after releasing the terminal, and once Go installs a
// handler for a signal, signal.Reset does not restore the kernel's default
// stop action.

// programSender is the part of *tea.Program that job-control handling uses.
type programSender interface {
	Send(tea.Msg)
}

// watchJobControlStops turns SIGTTIN into an orderly suspension: the program
// releases the terminal, the process group stops as if ctrl+z had been
// pressed, and the program restores the terminal and repaints once it is
// continued. The returned function stops watching.
func watchJobControlStops(ctx context.Context, program programSender) func() {
	// Releasing and restoring the terminal call tcsetattr, which raises
	// SIGTTOU from a background process group instead of succeeding.
	// Ignoring it lets the TUI hand the terminal back in cooked mode even
	// after the shell has taken the foreground.
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
			// Drop reads that raised SIGTTIN while suspending. If the
			// process is still in the background, the next read raises it
			// again.
			drainSignals(signals)
		}
	}()
	return func() {
		cancel()
		<-done
		signal.Stop(signals)
	}
}

// suspendProgram runs one suspension through the program's event loop. It
// reports false if the program ended before the suspension completed.
func suspendProgram(ctx context.Context, program programSender) bool {
	resumed := make(chan struct{})
	// tea.Exec releases the terminal, runs the command, then restores the
	// terminal and repaints, all on the event loop goroutine.
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

// stopProcess is a tea.ExecCommand that stops the process group the way
// ctrl+z does at a shell prompt, and returns once the group is continued.
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
