package auth

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestE2E_OAuthRefresh is opt-in because it consumes and rotates the real
// refresh token stored by `bits login`.
func TestE2E_OAuthRefresh(t *testing.T) {
	if os.Getenv("BITS_OAUTH_E2E") == "" {
		t.Skip("set BITS_OAUTH_E2E=1 after bits login")
	}
	store := KeyringStore{}
	before, err := store.Load()
	if err != nil {
		t.Fatalf("load OAuth session: %v", err)
	}
	if before.RefreshToken == "" {
		t.Fatal("stored OAuth session has no refresh token")
	}

	forced := before
	forced.Expiry = time.Now().Add(-time.Minute)
	if err := withSessionLock(context.Background(), store, func() error {
		return store.Save(forced)
	}); err != nil {
		t.Fatalf("persist forced-expiry session: %v", err)
	}
	source, err := NewSource(forced, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	accessToken, err := source.AccessToken(context.Background())
	if err != nil {
		t.Fatalf("refresh OAuth session: %v", err)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatalf("load refreshed OAuth session: %v", err)
	}
	if accessToken == "" || after.AccessToken != accessToken {
		t.Fatal("refreshed access token was not persisted")
	}
	if after.RefreshToken == "" || after.Expiry.Before(time.Now()) {
		t.Fatal("refreshed session is incomplete or already expired")
	}
	if after.AccessToken == before.AccessToken && after.RefreshToken == before.RefreshToken {
		t.Fatal("token endpoint returned an unchanged access/refresh token pair")
	}
}
