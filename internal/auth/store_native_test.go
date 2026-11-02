package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// This test uses an isolated credential in the real OS keyring. Run it in a
// fresh process because other store tests replace go-keyring's global provider.
func TestNativeKeyringSessionSize(t *testing.T) {
	const childEnv = "BITS_NATIVE_KEYRING_TEST_SERVICE"
	if service := os.Getenv(childEnv); service != "" {
		testNativeKeyringSessionSize(t, service)
		return
	}
	if os.Getenv("BITS_KEYRING_E2E") == "" {
		t.Skip("set BITS_KEYRING_E2E=1 to test the native credential store")
	}
	if runtime.GOOS == "linux" && !KeyringAvailable() {
		testNativeFileFallback(t)
		return
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	service := "com.datadog.bits-cli.test." + hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeKeyringSessionSize$")
	cmd.Env = append(os.Environ(), childEnv+"="+service)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native keyring test: %v\n%s", err, output)
	}
}

func testNativeFileFallback(t *testing.T) {
	store := Store{
		filePath:  filepath.Join(t.TempDir(), "oauth-session.json"),
		available: KeyringAvailable,
	}
	session := fileTestSession()
	session.AccessToken = strings.Repeat("a", 2550)
	if err := store.Save(session); err != nil {
		t.Fatalf("save file fallback: %v", err)
	}
	got, err := store.Load()
	if err != nil || !sameSession(got, session) {
		t.Fatalf("file fallback did not round trip: %v", err)
	}
	if _, err := os.Stat(store.filePath); err != nil {
		t.Fatalf("file fallback was not written: %v", err)
	}
}

func testNativeKeyringSessionSize(t *testing.T, service string) {
	const account = "oauth-session"
	t.Cleanup(func() {
		if err := keyring.Delete(service, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("remove test credential: %v", err)
		}
	})
	small := fileTestSession()
	if err := saveKeyringSession(service, account, small); err != nil {
		t.Fatalf("save small session: %v", err)
	}
	assertNativeSession(t, service, account, small)

	large := fileTestSession()
	large.AccessToken = strings.Repeat("a", 2550)
	raw, err := json.Marshal(large)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 2560 || len(raw) >= 3000 {
		t.Fatalf("large session is %d bytes; want between 2560 and 3000", len(raw))
	}
	err = saveKeyringSession(service, account, large)
	if runtime.GOOS == "windows" {
		if !errors.Is(err, keyring.ErrSetDataTooBig) || !strings.Contains(err.Error(), "OAuth session is too large for the OS credential store") {
			t.Fatalf("Windows oversized save error = %v", err)
		}
		if strings.Contains(err.Error(), large.AccessToken) || strings.Contains(err.Error(), large.RefreshToken) {
			t.Fatal("oversized save error exposed a token")
		}
		assertNativeSession(t, service, account, small)
	} else {
		if err != nil {
			t.Fatalf("save large session: %v", err)
		}
		assertNativeSession(t, service, account, large)
	}
	if err := keyring.Delete(service, account); err != nil {
		t.Fatalf("delete test credential: %v", err)
	}
	if _, err := keyring.Get(service, account); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("deleted credential remains readable: %v", err)
	}
}

func assertNativeSession(t *testing.T, service, account string, want Session) {
	t.Helper()
	raw, err := keyring.Get(service, account)
	if err != nil {
		t.Fatalf("read test credential: %v", err)
	}
	var got Session
	if err := json.Unmarshal([]byte(raw), &got); err != nil || !sameSession(got, want) {
		t.Fatalf("test credential did not round trip: %v", err)
	}
}
