package slack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Slack desktop app being absent used to surface as "exit status 1" from
// sqlite3, several layers below where the user could act on it.
func TestCheckInstalledDistinguishesAbsenceFromSignedOut(t *testing.T) {
	t.Run("app absent", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := checkInstalled()
		if err == nil {
			t.Fatal("expected an error when the Slack desktop app is not installed")
		}
		if !strings.Contains(err.Error(), "isn't installed") {
			t.Errorf("should say the app isn't installed, got %q", err)
		}
		if strings.Contains(err.Error(), "exit status") {
			t.Errorf("raw exit status leaked to the user: %q", err)
		}
	})

	t.Run("app present but signed out", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, "Library", "Application Support", "Slack"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := checkInstalled()
		if err == nil {
			t.Fatal("expected an error when there is no saved session")
		}
		if !strings.Contains(err.Error(), "sign in") {
			t.Errorf("should tell the user to sign in, got %q", err)
		}
	})

	t.Run("signed in", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, "Library", "Application Support", "Slack")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Cookies"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkInstalled(); err != nil {
			t.Errorf("a signed-in install should pass the check, got %v", err)
		}
	})
}
