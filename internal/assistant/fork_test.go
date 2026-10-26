package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestForkConversation(t *testing.T) {
	const source = "12345678-1234-1234-1234-123456781234"
	const target = "87654321-4321-4321-4321-210987654321"
	for _, status := range []int{201, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/api/v2/assistant/conversation/"+source+"/fork" || len(body) != 0 {
					t.Errorf("request: %s %s %q", r.Method, r.URL.Path, body)
				}
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"data":{"type":"conversation","id":%q,"attributes":{"conversation_id":%q,"title":"Forked: example"}}}`, target, target)
			})
			c.MaxRetries = 3
			result, err := c.ForkConversation(context.Background(), source)
			if status == 201 {
				if err != nil || result.ID != target || result.ConversationID != target {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("expected error")
			}
			if calls != 1 {
				t.Fatalf("fork sent %d times", calls)
			}
		})
	}
}

func TestForkRejectsMalformedResource(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":{"type":"other","id":"a","attributes":{"conversation_id":"a"}}}`, `{"data":{"type":"conversation","id":"a","attributes":{"conversation_id":"b"}}}`} {
		c := testClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201); _, _ = fmt.Fprint(w, body) })
		if _, err := c.ForkConversation(context.Background(), "source"); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

type forkTransport func(*http.Request) (*http.Response, error)

func (f forkTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestForkTransportFailureIsNotRetried(t *testing.T) {
	calls := 0
	c := &Client{BaseURL: "https://example.test", APIKey: "api", AppKey: "app", MaxRetries: 3, HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: forkTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) < 55*time.Second {
			t.Error("fork deadline shorter than server budget")
		}
		return nil, errors.New("connection lost")
	})}}
	if _, err := c.ForkConversation(context.Background(), "source"); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if c.HTTPClient.Timeout != 30*time.Second {
		t.Fatal("changed shared client timeout")
	}
}

func TestHistoryDecodesForkProvenance(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"type":"conversation-history-response","attributes":{"forked_from":{"conversation_id":"source","message_id":"source-message","fork_message_id":"copied-message","source_title":"Current title"}}}}`)
	})
	response, err := c.ConversationHistory(context.Background(), ConversationHistoryInput{ConversationID: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	fork := response.Data.Attributes.ForkedFrom
	if fork == nil || fork.ForkMessageID != "copied-message" || fork.SourceTitle != "Current title" || fork.ConversationID != "source" {
		t.Fatalf("fork=%+v", fork)
	}
}
