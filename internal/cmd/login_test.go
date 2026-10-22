package cmd

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadCallbackURLsCancellationLeavesStdinOpen(t *testing.T) {
	in, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	defer func() { _ = out.Close() }()
	urls := make(chan string)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readCallbackURLs(ctx, in, urls)
	// Exercise the adapter before cancellation; do not only stop a reader
	// whose goroutine may not have started yet.
	if _, err := out.WriteString("pasted address\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-urls:
	case <-time.After(time.Second):
		t.Fatal("input reader did not deliver the callback")
	}
	cancel()
	if _, err := out.WriteString("still open\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-urls:
		if open {
			t.Fatal("callback delivered after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not stop after input resumed")
	}
}

func TestReadCallbackURLsLines(t *testing.T) {
	input := strings.NewReader(" \r\n https://localhost/callback?code=first \r\nhttps://localhost/callback?code=second\n")
	urls := make(chan string)
	readCallbackURLs(context.Background(), input, urls)
	var lines []string
	for line := range urls {
		lines = append(lines, line)
	}
	want := []string{"https://localhost/callback?code=first", "https://localhost/callback?code=second"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("callbacks = %q, want %q", lines, want)
	}
}
