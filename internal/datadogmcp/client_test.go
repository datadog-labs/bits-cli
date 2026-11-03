package datadogmcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type tokenSource struct{ rejected string }

func (*tokenSource) AccessToken(context.Context) (string, error) { return "test-token", nil }
func (s *tokenSource) RejectAccessToken(_ context.Context, token string) error {
	s.rejected = token
	return nil
}

func TestAuthTransport(t *testing.T) {
	source := &tokenSource{}
	for _, oauth := range []bool{false, true} {
		transport := &mcpAuthTransport{apiKey: "api-key", appKey: "app-key"}
		if oauth {
			transport.token = source
		}
		transport.next = transportFunc(func(r *http.Request) (*http.Response, error) {
			if oauth {
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("DD-API-KEY") != "" {
					t.Fatal("incorrect OAuth headers")
				}
			} else if r.Header.Get("DD-API-KEY") != "api-key" || r.Header.Get("DD-APPLICATION-KEY") != "app-key" {
				t.Fatal("missing API key headers")
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private error"))}, nil
		})
		request, _ := http.NewRequest(http.MethodPost, "https://mcp.datadoghq.com/v1/mcp", nil)
		response, err := transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.Body != http.NoBody || request.Header.Get("Authorization") != "" {
			t.Fatal("transport leaked response or mutated request")
		}
	}
	if source.rejected != "test-token" {
		t.Fatal("rejected token was not reported")
	}
}

func TestSharedSessionRestrictsTools(t *testing.T) {
	client := fixtureClient(t, nil)
	for _, name := range []string{"get_widget", "_dd_gff"} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil || result == nil || result.StructuredContent == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "_dd_wfd"}); err == nil {
		t.Fatal("accepted tool not advertised as read-only")
	}
	if _, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "delete_dashboard"}); err == nil {
		t.Fatal("accepted unsupported tool")
	}
	if _, err := New(&assistant.Client{BaseURL: "https://api.example.org"}); err == nil {
		t.Fatal("accepted untrusted site")
	}
}

func fixtureClient(t *testing.T, wrap func(http.Handler) http.Handler) *Client {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	t.Cleanup(func() {
		for session := range server.Sessions() {
			_ = session.Close()
		}
	})
	for _, name := range []string{"get_widget", "_dd_gff", "_dd_wfd"} {
		mcp.AddTool(server, &mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: name != "_dd_wfd"}},
			func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				return nil, map[string]any{"value": "fixture"}, nil
			})
	}
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	if wrap != nil {
		handler = wrap(handler)
	}
	client, err := New(&assistant.Client{BaseURL: "https://api.datadoghq.com", APIKey: "api-key", AppKey: "app-key"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	client.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "mcp.datadoghq.com" || r.Header.Get("DD-API-KEY") != "api-key" {
			t.Fatal("incorrect endpoint or authentication")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})
	return client
}

func TestSessionRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		terminal bool
	}{
		{"unauthorized", http.StatusUnauthorized, "private error", true},
		{"missing", http.StatusNotFound, "session missing", true},
		{"malformed-json", http.StatusOK, "{", true},
		{"transient", http.StatusServiceUnavailable, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var fail atomic.Bool
				var posts atomic.Int32
				client := fixtureClient(t, func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Method == http.MethodPost {
							posts.Add(1)
							if fail.Load() {
								w.Header().Set("Content-Type", "application/json")
								w.WriteHeader(tc.status)
								_, _ = io.WriteString(w, tc.body)
								return
							}
						}
						next.ServeHTTP(w, r)
					})
				})
				params := &mcp.CallToolParams{Name: "get_widget", Arguments: map[string]any{}}
				if _, err := client.CallTool(t.Context(), params); err != nil {
					t.Fatal(err)
				}
				old := client.session
				before := posts.Load()
				fail.Store(true)
				if _, err := client.CallTool(t.Context(), params); err == nil {
					t.Fatal("expected call failure")
				}
				synctest.Wait()
				if posts.Load() != before+1 {
					t.Fatal("failed request was replayed")
				}
				if tc.terminal && (client.session != nil || client.allowed != nil) {
					t.Fatal("terminated session or its permissions were retained")
				}
				fail.Store(false)
				if _, err := client.CallTool(t.Context(), params); err != nil {
					t.Fatalf("explicit retry: %v", err)
				}
				replacement := client.session
				if (replacement != old) != tc.terminal {
					t.Fatal("session replacement does not match failure type")
				}
				if tc.terminal {
					client.retire(old)
					if client.session != replacement || !client.allowed["get_widget"] {
						t.Fatal("late retirement evicted the replacement or its permissions")
					}
				}
			})
		})
	}
}

func TestCancelWhileWaitingForInitialization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		client := fixtureClient(t, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					select {
					case <-entered:
					default:
						close(entered)
						<-release
					}
				}
				next.ServeHTTP(w, r)
			})
		})
		params := &mcp.CallToolParams{Name: "get_widget", Arguments: map[string]any{}}
		first := make(chan error, 1)
		go func() {
			_, err := client.CallTool(t.Context(), params)
			first <- err
		}()
		<-entered
		ctx, cancel := context.WithCancel(t.Context())
		second := make(chan error, 1)
		go func() {
			_, err := client.CallTool(ctx, params)
			second <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-second:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("waiting call: %v", err)
			}
		default:
			t.Error("canceled caller is still waiting for initialization")
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatalf("independent initialization failed: %v", err)
		}
	})
}
