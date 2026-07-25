package macos

import (
	"errors"
	"strings"
	"testing"
)

// A missing Keychain item is the everyday case for someone who has only one of
// the two apps installed, so it has to be reported as an absence callers can
// branch on — not as a bare exit status.
func TestSecretMissingItemIsIdentifiable(t *testing.T) {
	_, err := Secret("lurk test — no such Safe Storage item")
	if err == nil {
		t.Fatal("expected an error for a service name that cannot exist")
	}
	if !errors.Is(err, ErrNoKeychainItem) {
		t.Errorf("missing item should match ErrNoKeychainItem, got %v", err)
	}
	// The service name belongs in the message; a user reading it should be able
	// to tell which app's key is missing.
	if !strings.Contains(err.Error(), "lurk test") {
		t.Errorf("error should name the service it looked for, got %q", err)
	}
	if strings.Contains(err.Error(), "exit status") {
		t.Errorf("raw exit status leaked to the user: %q", err)
	}
}
