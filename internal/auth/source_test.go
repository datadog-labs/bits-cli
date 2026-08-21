package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memoryStore struct {
	mu      sync.Mutex
	session Session
	saves   int
	err     error
}

func (s *memoryStore) Load() (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session, s.err
}

func (s *memoryStore) Save(session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.session = session
	s.saves++
	return nil
}

func (s *memoryStore) Delete() error { return nil }

func TestSourceRefreshesOnceAndPersistsRotatedToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer server.Close()

	store := &memoryStore{}
	source := &Source{
		config: SiteConfig{
			Site:        DefaultStagingSite,
			ClientID:    "client",
			TokenURL:    server.URL,
			RedirectURI: DefaultRedirectURI,
		},
		store:      store,
		httpClient: server.Client(),
		session: Session{
			Site:         DefaultStagingSite,
			ClientID:     "client",
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(time.Minute),
		},
	}

	const callers = 12
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := source.AccessToken()
			if err != nil {
				errs <- err
				return
			}
			if token != "new-access" {
				errs <- fmt.Errorf("token = %q", token)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("refresh requests = %d, want 1", got)
	}
	if store.saves != 1 || store.session.RefreshToken != "new-refresh" {
		t.Errorf("stored session = %#v, saves = %d", store.session, store.saves)
	}
}

func TestSourceKeepsRotatedTokenWhenPersistenceTemporarilyFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	store := &memoryStore{err: fmt.Errorf("keyring unavailable")}
	source := &Source{
		config:     SiteConfig{Site: DefaultStagingSite, ClientID: "client", TokenURL: server.URL},
		store:      store,
		httpClient: server.Client(),
		session: Session{
			Site: DefaultStagingSite, ClientID: "client", AccessToken: "old-access",
			RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
		},
	}
	if token, err := source.AccessToken(); err != nil || token != "new-access" {
		t.Fatalf("first AccessToken = %q, %v", token, err)
	}
	if !source.dirty {
		t.Fatal("rotated token should remain dirty after failed persistence")
	}
	store.err = nil
	if token, err := source.AccessToken(); err != nil || token != "new-access" {
		t.Fatalf("second AccessToken = %q, %v", token, err)
	}
	if source.dirty || store.session.RefreshToken != "new-refresh" {
		t.Fatalf("rotated session was not persisted: %#v", store.session)
	}
}

func TestSourceRefreshesExpiredDirtyTokenWhenStoreRemainsUnavailable(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"next-access","refresh_token":"next-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	source := &Source{
		config:     SiteConfig{Site: DefaultStagingSite, ClientID: "client", TokenURL: server.URL},
		store:      &memoryStore{err: fmt.Errorf("keyring unavailable")},
		httpClient: server.Client(),
		session: Session{
			Site: DefaultStagingSite, ClientID: "client", AccessToken: "dirty-expired",
			RefreshToken: "dirty-refresh", Expiry: time.Now().Add(-time.Minute),
		},
		dirty: true,
	}
	token, err := source.AccessToken()
	if err != nil || token != "next-access" {
		t.Fatalf("AccessToken = %q, %v", token, err)
	}
	if requests.Load() != 1 || !source.dirty {
		t.Fatalf("requests = %d, dirty = %v", requests.Load(), source.dirty)
	}
}

func TestSourceRequiresReauthWithoutRefreshToken(t *testing.T) {
	source := &Source{
		config: SiteConfig{},
		store:  &memoryStore{},
		session: Session{
			Site:        DefaultStagingSite,
			ClientID:    "client",
			AccessToken: "expired",
			Expiry:      time.Now().Add(-time.Minute),
		},
	}
	if _, err := source.AccessToken(); err == nil {
		t.Fatal("AccessToken succeeded without a refresh token")
	}
}
