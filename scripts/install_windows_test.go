package scripts

import (
	"crypto/rand"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// isolateUserPath gives bits install a scratch HKCU key to add its PATH entry
// to, so the test never changes the real user PATH.
func isolateUserPath(t *testing.T) {
	t.Helper()
	path := `Software\bits-cli-test-` + rand.Text()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil {
			t.Errorf("delete %s: %v", path, err)
		}
	})
	t.Setenv("BITS_TEST_USER_ENVIRONMENT", path)
}
