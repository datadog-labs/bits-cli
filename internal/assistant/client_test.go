package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testClient points a Client at h with the httptest server's client for both
// the streaming and non-streaming paths. StreamIdleTimeout is left zero so it
// defaults (90s); idle-specific tests set it explicitly.
type staticAccessToken string

func (t staticAccessToken) AccessToken(context.Context) (string, error) { return string(t), nil }

type rejectingAccessToken struct {
	token    string
	rejected string
	calls    int
	err      error
}

func (t *rejectingAccessToken) AccessToken(context.Context) (string, error) { return t.token, nil }
func (t *rejectingAccessToken) RejectAccessToken(_ context.Context, token string) error {
	t.calls++
	t.rejected = token
	return t.err
}

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		BaseURL:      srv.URL,
		APIKey:       "api",
		AppKey:       "app",
		HTTPClient:   srv.Client(),
		StreamClient: srv.Client(),
	}
}

// textLine builds one wire line: an assistant-response carrying a markdown
// fragment. content is embedded raw, so callers control its size.
func textLine(convID, content string) string {
	return fmt.Sprintf(
		`{"data":{"type":"assistant-response","attributes":{"conversation_id":%q,"structured_message":{"role":"assistant","message_id":"m1","content":{"type":"markdown_fragment","content":%q}}}}}`+"\n\n",
		convID, content,
	)
}

// keepaliveLine builds one server keepalive line (no structured_message).
func keepaliveLine(convID string) string {
	return fmt.Sprintf(`{"data":{"type":"assistant-response","attributes":{"conversation_id":%q,"structured_message":null}}}`+"\n\n", convID)
}

func writeStream(t *testing.T, w http.ResponseWriter, lines ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, ln := range lines {
		if _, err := w.Write([]byte(ln)); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func TestDo_MapsStatusToSentinel(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, ErrBadRequest},
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrForbidden},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusConflict, ErrConflict},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusInternalServerError, ErrServer},
		{http.StatusServiceUnavailable, ErrServer},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"errors":[{"status":"%d","title":%q,"detail":"boom"}]}`, tc.status, http.StatusText(tc.status))
			})
			_, err := c.ConversationHistory(context.Background(), ConversationHistoryInput{ConversationID: "cid"})
			if err == nil {
				t.Fatalf("expected error for status %d", tc.status)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.want)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %T", err)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
			if apiErr.Detail != "boom" {
				t.Errorf("Detail = %q, want %q", apiErr.Detail, "boom")
			}
		})
	}
}

func TestDo_FallsBackToRawBodyWhenNotJSONAPI(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "upstream exploded")
	})
	_, err := c.UserConversations(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if apiErr.Detail != "upstream exploded" {
		t.Errorf("Detail = %q, want raw body", apiErr.Detail)
	}
	if !errors.Is(err, ErrServer) {
		t.Errorf("502 should unwrap to ErrServer")
	}
}

func TestSend_StreamsTextAndConversationID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w, textLine("conv-xyz", "hello "), textLine("conv-xyz", "world"))
	})
	var got strings.Builder
	convID, err := c.Send(context.Background(), "hi", SendOptions{}, func(ar AssistantResponse) error {
		if ct := ar.Data.Attributes.StructuredMessage.Content; ct.Kind() == KindText {
			got.WriteString(ct.TextBody())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if convID != "conv-xyz" {
		t.Errorf("convID = %q, want conv-xyz", convID)
	}
	if got.String() != "hello world" {
		t.Errorf("text = %q, want %q", got.String(), "hello world")
	}
}

func TestSend_SurfacesInBandError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w,
			textLine("conv-1", "partial answer"),
			`{"errors":[{"status":"504","title":"Request timed out","detail":"The assistant stopped producing updates. Please retry."}]}`+"\n\n",
		)
	})
	var lines int
	convID, err := c.Send(context.Background(), "hi", SendOptions{}, func(ar AssistantResponse) error {
		if ar.Data.Attributes.StructuredMessage.Content.Kind() == KindText {
			lines++
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected an in-band error to surface")
	}
	if !errors.Is(err, ErrServer) {
		t.Errorf("504 in-band error should unwrap to ErrServer, got %v", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode != http.StatusGatewayTimeout {
			t.Errorf("StatusCode = %d, want 504", apiErr.StatusCode)
		}
		if !strings.Contains(apiErr.Detail, "stopped producing updates") {
			t.Errorf("Detail = %q", apiErr.Detail)
		}
	} else {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if lines != 1 {
		t.Errorf("expected the pre-error content line to be delivered, got %d", lines)
	}
	if convID != "conv-1" {
		t.Errorf("convID = %q, want conv-1", convID)
	}
}

func TestSend_IdleTimeout(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeStream(t, w, textLine("conv-1", "first")) // then go silent
		<-r.Context().Done()                           // block until the client gives up
	})
	c.StreamIdleTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := c.Send(context.Background(), "hi", SendOptions{}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an idle-timeout error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 idle-timeout APIError, got %v", err)
	}
	if !strings.Contains(apiErr.Detail, "no data received") {
		t.Errorf("Detail = %q, want idle-timeout message", apiErr.Detail)
	}
	if elapsed > 2*time.Second {
		t.Errorf("idle timeout took too long: %s", elapsed)
	}
}

func TestSend_CallerCancellation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeStream(t, w, textLine("conv-1", "first"))
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	_, err := c.Send(ctx, "hi", SendOptions{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSend_HandlesLargeLine(t *testing.T) {
	// Comfortably under the 32 MiB default cap but well past the old 8 MiB
	// limit: a legitimately large line must still be delivered.
	big := strings.Repeat("a", 9<<20)
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w, textLine("conv-1", big))
	})
	var got int
	_, err := c.Send(context.Background(), "hi", SendOptions{}, func(ar AssistantResponse) error {
		got = len(ar.Data.Attributes.StructuredMessage.Content.TextBody())
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got != len(big) {
		t.Errorf("received content length = %d, want %d", got, len(big))
	}
}

func TestSend_SkipsKeepalive(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w,
			keepaliveLine("conv-1"),
			textLine("conv-1", "real content"),
			keepaliveLine("conv-1"),
		)
	})
	var calls int
	var got string
	convID, err := c.Send(context.Background(), "hi", SendOptions{}, func(ar AssistantResponse) error {
		calls++
		got = ar.Data.Attributes.StructuredMessage.Content.TextBody()
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if calls != 1 {
		t.Errorf("callback invoked %d times, want 1 (keepalives filtered)", calls)
	}
	if got != "real content" {
		t.Errorf("content = %q, want %q", got, "real content")
	}
	if convID != "conv-1" {
		t.Errorf("convID = %q, want conv-1 (captured even from keepalives)", convID)
	}
}

func TestSend_KeepalivesResetIdleTimeout(t *testing.T) {
	// The stream's total duration far exceeds StreamIdleTimeout, but periodic
	// keepalives bridge every gap, so the turn completes rather than tripping
	// the idle watchdog. This mirrors the real server holding a long "thinking"
	// turn open with 30s pings. Keepalives are the only thing that makes this
	// safe, so a pass proves they reset the timer; they are still filtered from
	// the callback, so only the two real content lines are delivered.
	const (
		idle  = 200 * time.Millisecond
		gap   = 40 * time.Millisecond // 5x margin under idle
		pings = 10
	)
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(textLine("conv-1", "part1"))
		for range pings {
			time.Sleep(gap)
			write(keepaliveLine("conv-1"))
		}
		time.Sleep(gap)
		write(textLine("conv-1", "part2"))
	})
	c.StreamIdleTimeout = idle

	var got strings.Builder
	var calls int
	start := time.Now()
	_, err := c.Send(context.Background(), "hi", SendOptions{}, func(ar AssistantResponse) error {
		calls++
		got.WriteString(ar.Data.Attributes.StructuredMessage.Content.TextBody())
		return nil
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Send: %v (keepalives should have kept the stream alive)", err)
	}
	if got.String() != "part1part2" || calls != 2 {
		t.Errorf("got %q in %d callbacks, want %q in 2 (keepalives filtered)", got.String(), calls, "part1part2")
	}
	if elapsed < idle {
		t.Errorf("stream finished in %s, expected to outlast the %s idle timeout", elapsed, idle)
	}
}

func TestSend_RejectsOversizedLine(t *testing.T) {
	// A line past MaxLineBytes must fail cleanly rather than grow unbounded.
	big := strings.Repeat("a", 256*1024)
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeStream(t, w, textLine("conv-1", big))
	})
	c.MaxLineBytes = 128 * 1024
	_, err := c.Send(context.Background(), "hi", SendOptions{}, nil)
	if err == nil {
		t.Fatal("expected an error for an oversized line")
	}
	if !strings.Contains(err.Error(), "exceeds max size") {
		t.Errorf("error = %v, want it to mention the size cap", err)
	}
}

func TestDo_RetriesTransientStatusThenSucceeds(t *testing.T) {
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) <= 2 { // fail the first two attempts
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data":{"attributes":{"conversations":[]}}}`)
	})
	c.MaxRetries = 2
	c.RetryBaseDelay = time.Millisecond
	if _, err := c.UserConversations(context.Background()); err != nil {
		t.Fatalf("UserConversations: %v", err)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (2 retries + success)", got)
	}
}

func TestDo_ExhaustsRetriesThenReturnsError(t *testing.T) {
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	c.MaxRetries = 2
	c.RetryBaseDelay = time.Millisecond
	_, err := c.UserConversations(context.Background())
	if !errors.Is(err, ErrServer) {
		t.Fatalf("want ErrServer, got %v", err)
	}
	if got := n.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestDo_DoesNotRetryNonRetryableStatus(t *testing.T) {
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"errors":[{"status":"404","detail":"nope"}]}`)
	})
	c.MaxRetries = 3
	c.RetryBaseDelay = time.Millisecond
	_, err := c.ConversationHistory(context.Background(), ConversationHistoryInput{ConversationID: "cid"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (4xx is not retried)", got)
	}
}

func TestSend_DoesNotRetry(t *testing.T) {
	// The streaming POST must never retry: the API has no idempotency key, so a
	// retried turn would be recorded twice.
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"errors":[{"status":"503","detail":"busy"}]}`)
	})
	c.MaxRetries = 5
	c.RetryBaseDelay = time.Millisecond
	_, err := c.Send(context.Background(), "hi", SendOptions{}, nil)
	if !errors.Is(err, ErrServer) {
		t.Fatalf("want ErrServer, got %v", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("POST attempts = %d, want 1 (streaming POST is never retried)", got)
	}
}

func TestUnauthorizedMarksOAuthTokenStaleWithoutRetry(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "idempotent"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"errors":[{"status":"401"}]}`)
			})
			source := &rejectingAccessToken{token: "rejected-token"}
			c.TokenSource = source
			c.MaxRetries = 3
			var requestErr error
			if streaming {
				_, requestErr = c.Send(context.Background(), "hi", SendOptions{}, nil)
			} else {
				_, requestErr = c.ExperimentalToolFlags(context.Background())
			}
			if !errors.Is(requestErr, ErrUnauthorized) {
				t.Fatalf("error = %v, want ErrUnauthorized", requestErr)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want no automatic retry", requests.Load())
			}
			if source.calls != 1 || source.rejected != "rejected-token" {
				t.Fatalf("rejections = %d, token = %q", source.calls, source.rejected)
			}
		})
	}
}

func TestUnauthorizedSurfacesTokenRejectionPersistenceError(t *testing.T) {
	storeErr := errors.New("keyring unavailable")
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"errors":[{"status":"401"}]}`)
	})
	c.TokenSource = &rejectingAccessToken{token: "rejected-token", err: storeErr}
	_, err := c.ExperimentalToolFlags(context.Background())
	if !errors.Is(err, ErrUnauthorized) || !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want unauthorized joined with store error", err)
	}
}

func TestRetryAfterDelay(t *testing.T) {
	if d, ok := retryAfterDelay(http.Header{"Retry-After": {"2"}}); !ok || d != 2*time.Second {
		t.Errorf("delta-seconds: got %v ok=%v, want 2s true", d, ok)
	}
	if _, ok := retryAfterDelay(http.Header{}); ok {
		t.Error("absent Retry-After should report ok=false")
	}
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	if d, ok := retryAfterDelay(http.Header{"Retry-After": {future}}); !ok || d <= 0 || d > 4*time.Second {
		t.Errorf("http-date: got %v ok=%v, want ~3s true", d, ok)
	}
}

func TestNewRequest_SetsOAuthBearerWithoutAPIKeys(t *testing.T) {
	var authorization, apiKey, appKey string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		apiKey = r.Header.Get("DD-API-KEY")
		appKey = r.Header.Get("DD-APPLICATION-KEY")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data":{"attributes":{"flags":{}}}}`)
	})
	c.TokenSource = staticAccessToken("oauth-token")
	if _, err := c.ExperimentalToolFlags(context.Background()); err != nil {
		t.Fatalf("ExperimentalToolFlags: %v", err)
	}
	if authorization != "Bearer oauth-token" {
		t.Errorf("Authorization = %q", authorization)
	}
	if apiKey != "" || appKey != "" {
		t.Errorf("API key headers must be absent in OAuth mode: %q/%q", apiKey, appKey)
	}
}

func TestNewRequest_SetsAuthHeaders(t *testing.T) {
	var gotAPI, gotApp, gotContentType string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAPI = r.Header.Get("DD-API-KEY")
		gotApp = r.Header.Get("DD-APPLICATION-KEY")
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"data":{"attributes":{"flags":{}}}}`)
	})
	if _, err := c.ExperimentalToolFlags(context.Background()); err != nil {
		t.Fatalf("ExperimentalToolFlags: %v", err)
	}
	if gotAPI != "api" || gotApp != "app" {
		t.Errorf("auth headers = %q/%q, want api/app", gotAPI, gotApp)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
}
