package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	txMu sync.Mutex
	mu   sync.Mutex

	session      Session
	present      bool
	saves        int
	deletes      int
	saveFailures int
	loadErr      error
	saveErr      error
	deleteErr    error
}

func newMemoryStore(session Session) *memoryStore {
	return &memoryStore{session: session, present: true}
}

func (s *memoryStore) Load() (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return Session{}, s.loadErr
	}
	if !s.present {
		return Session{}, ErrNoSession
	}
	return s.session, nil
}

func (s *memoryStore) Save(session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveFailures > 0 {
		s.saveFailures--
		return s.saveErr
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	s.session = session
	s.present = true
	s.saves++
	return nil
}

func (s *memoryStore) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.session = Session{}
	s.present = false
	s.deletes++
	return nil
}

func (s *memoryStore) WithSessionLock(ctx context.Context, fn func() error) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func expiredSession() Session {
	return Session{
		Site:         DefaultStagingSite,
		ClientID:     "client",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Minute),
	}
}

func refreshServer(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != "client" {
			t.Errorf("refresh form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
}

func testSource(t *testing.T, session Session, store CredentialStore, tokenURL string, client *http.Client) *Source {
	t.Helper()
	return &Source{
		config: SiteConfig{
			Site:        DefaultStagingSite,
			ClientID:    "client",
			TokenURL:    tokenURL,
			RedirectURI: DefaultRedirectURI,
		},
		store:      store,
		httpClient: client,
		session:    session,
	}
}

func TestIndependentSourcesRefreshOnceAndAdoptDurableRotation(t *testing.T) {
	var requests atomic.Int32
	server := refreshServer(t, &requests)
	defer server.Close()

	initial := expiredSession()
	store := newMemoryStore(initial)
	sources := []*Source{
		testSource(t, initial, store, server.URL, server.Client()),
		testSource(t, initial, store, server.URL, server.Client()),
	}

	start := make(chan struct{})
	errs := make(chan error, len(sources))
	for _, source := range sources {
		go func(source *Source) {
			<-start
			token, err := source.AccessToken(context.Background())
			if err == nil && token != "new-access" {
				err = fmt.Errorf("token = %q", token)
			}
			errs <- err
		}(source)
	}
	close(start)
	for range sources {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("refresh requests = %d, want 1", got)
	}
	if store.saves != 1 || store.session.RefreshToken != "new-refresh" {
		t.Fatalf("stored session = %#v, saves = %d", store.session, store.saves)
	}
}

func TestSourceSerializesConcurrentCallsInOneProcess(t *testing.T) {
	var requests atomic.Int32
	server := refreshServer(t, &requests)
	defer server.Close()

	initial := expiredSession()
	store := newMemoryStore(initial)
	source := testSource(t, initial, store, server.URL, server.Client())
	const callers = 12
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := source.AccessToken(context.Background())
			if err != nil {
				errs <- err
			} else if token != "new-access" {
				errs <- fmt.Errorf("token = %q", token)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("refresh requests = %d, want 1", requests.Load())
	}
}

func TestSourceRetriesRotatedTokenPersistence(t *testing.T) {
	var requests atomic.Int32
	server := refreshServer(t, &requests)
	defer server.Close()

	initial := expiredSession()
	store := newMemoryStore(initial)
	store.saveFailures = 2
	store.saveErr = errors.New("keyring unavailable")
	source := testSource(t, initial, store, server.URL, server.Client())

	// Clear the persistent error after the configured transient failures.
	go func() {
		time.Sleep(100 * time.Millisecond)
		store.mu.Lock()
		store.saveErr = nil
		store.mu.Unlock()
	}()
	token, err := source.AccessToken(context.Background())
	if err != nil || token != "new-access" {
		t.Fatalf("AccessToken = %q, %v", token, err)
	}
	if requests.Load() != 1 || source.dirty || store.session.RefreshToken != "new-refresh" {
		t.Fatalf("requests = %d, dirty = %v, stored = %#v", requests.Load(), source.dirty, store.session)
	}
}

func TestSourceKeepsRotatedTokenAndRetriesWithoutSecondRefresh(t *testing.T) {
	var requests atomic.Int32
	server := refreshServer(t, &requests)
	defer server.Close()

	initial := expiredSession()
	store := newMemoryStore(initial)
	store.saveErr = errors.New("keyring unavailable")
	source := testSource(t, initial, store, server.URL, server.Client())

	if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrSessionNotDurable) {
		t.Fatalf("first error = %v, want ErrSessionNotDurable", err)
	}
	if !source.dirty || source.session.RefreshToken != "new-refresh" || !sameSession(store.session, initial) {
		t.Fatalf("dirty source = %#v, durable = %#v", source.session, store.session)
	}

	store.mu.Lock()
	store.saveErr = nil
	store.mu.Unlock()
	token, err := source.AccessToken(context.Background())
	if err != nil || token != "new-access" {
		t.Fatalf("second AccessToken = %q, %v", token, err)
	}
	if requests.Load() != 1 || source.dirty || store.session.RefreshToken != "new-refresh" {
		t.Fatalf("requests = %d, dirty = %v, stored = %#v", requests.Load(), source.dirty, store.session)
	}
}

func TestDirtySourceNeverOverwritesReplacementOrDeletion(t *testing.T) {
	initial := expiredSession()
	rotated := initial
	rotated.AccessToken = "rotated-access"
	rotated.RefreshToken = "rotated-refresh"
	rotated.Expiry = time.Now().Add(time.Hour)
	replacement := initial
	replacement.AccessToken = "replacement-access"
	replacement.RefreshToken = "replacement-refresh"
	replacement.Expiry = time.Now().Add(time.Hour)

	t.Run("adopts replacement", func(t *testing.T) {
		store := newMemoryStore(replacement)
		source := testSource(t, rotated, store, "", http.DefaultClient)
		source.dirty = true
		source.persistBase = initial
		token, err := source.AccessToken(context.Background())
		if err != nil || token != "replacement-access" {
			t.Fatalf("AccessToken = %q, %v", token, err)
		}
		if store.saves != 0 || !sameSession(store.session, replacement) {
			t.Fatalf("replacement overwritten: %#v", store.session)
		}
	})

	t.Run("does not resurrect deletion", func(t *testing.T) {
		store := &memoryStore{}
		source := testSource(t, rotated, store, "", http.DefaultClient)
		source.dirty = true
		source.persistBase = initial
		if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrReauthRequired) {
			t.Fatalf("error = %v, want ErrReauthRequired", err)
		}
		if store.present || store.saves != 0 {
			t.Fatalf("deleted session was resurrected: %#v", store.session)
		}
	})
}

func TestLogoutWaitsForRefreshAndDeletesRotatedSession(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var revokedToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			close(started)
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
		case "/oauth2/v1/revoke":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			revokedToken = r.Form.Get("token")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})

	initial := expiredSession()
	store := newMemoryStore(initial)
	source := testSource(t, initial, store, server.URL+"/token", client)
	refreshDone := make(chan error, 1)
	go func() {
		_, err := source.AccessToken(context.Background())
		refreshDone <- err
	}()
	<-started

	type logoutResult struct {
		had       bool
		revokeErr error
		err       error
	}
	logoutDone := make(chan logoutResult, 1)
	go func() {
		had, revokeErr, err := Logout(context.Background(), store, client)
		logoutDone <- logoutResult{had: had, revokeErr: revokeErr, err: err}
	}()
	select {
	case result := <-logoutDone:
		t.Fatalf("logout bypassed refresh transaction: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-refreshDone; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	result := <-logoutDone
	if result.err != nil || result.revokeErr != nil || !result.had {
		t.Fatalf("logout result = %#v", result)
	}
	if store.present || revokedToken != "new-refresh" {
		t.Fatalf("store present = %v, revoked = %q", store.present, revokedToken)
	}
}

func TestSourceInvalidGrantDeletesSessionAndRequiresLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"secret server detail"}`)
	}))
	defer server.Close()

	initial := expiredSession()
	store := newMemoryStore(initial)
	source := testSource(t, initial, store, server.URL, server.Client())
	_, err := source.AccessToken(context.Background())
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want ErrReauthRequired", err)
	}
	if store.present || store.deletes != 1 {
		t.Fatalf("store present = %v, deletes = %d", store.present, store.deletes)
	}
	if strings.Contains(err.Error(), "secret server detail") {
		t.Fatalf("error leaked server detail: %v", err)
	}
}

func TestSourceSanitizesRefreshServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"error":"temporarily_unavailable","error_description":"do not leak server-secret"}`)
	}))
	defer server.Close()
	initial := expiredSession()
	store := newMemoryStore(initial)
	source := testSource(t, initial, store, server.URL, server.Client())
	_, err := source.AccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 503 (temporarily_unavailable)") || strings.Contains(err.Error(), "server-secret") {
		t.Fatalf("error = %v", err)
	}
	if !store.present || store.deletes != 0 {
		t.Fatal("transient refresh error removed the stored session")
	}
}

func TestSourceRequiresReauthWithoutRefreshToken(t *testing.T) {
	initial := expiredSession()
	initial.RefreshToken = ""
	store := newMemoryStore(initial)
	source := testSource(t, initial, store, "", http.DefaultClient)
	if _, err := source.AccessToken(context.Background()); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("error = %v, want ErrReauthRequired", err)
	}
	if store.present {
		t.Fatal("unusable session remains stored")
	}
}

func TestSourceRefreshHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"late","refresh_token":"late-refresh","expires_in":3600}`)
		}
	}))
	defer server.Close()
	initial := expiredSession()
	store := newMemoryStore(initial)
	source := testSource(t, initial, store, server.URL, server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := source.AccessToken(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("cancellation took %s", elapsed)
	}
}
