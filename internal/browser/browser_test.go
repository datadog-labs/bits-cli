package browser

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOpenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Open(ctx, "https://app.datadoghq.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context cancellation", err)
	}
}

func TestLauncherCommand(t *testing.T) {
	target := "https://app.datadoghq.com/ask/conversation-1"
	tests := []struct {
		goos    string
		command string
		args    []string
	}{
		{goos: "darwin", command: "open", args: []string{target}},
		{goos: "windows", command: "rundll32", args: []string{"url.dll,FileProtocolHandler", target}},
		{goos: "linux", command: "xdg-open", args: []string{target}},
		{goos: "freebsd", command: "xdg-open", args: []string{target}},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			command, args := launcherCommand(test.goos, target)
			if command != test.command || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("launcherCommand() = %q, %v, want %q, %v", command, args, test.command, test.args)
			}
			if strings.Join(args, " ") == "" {
				t.Fatal("launcher omitted the target")
			}
		})
	}
}
