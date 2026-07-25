package signal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Not having Signal Desktop is a normal state, not a crash — and the message
// has to say so plainly, since `lurk summary` surfaces it verbatim to explain
// why a source is missing from the digest.
func TestCheckInstalledDistinguishesAbsenceFromEmptiness(t *testing.T) {
	t.Run("app absent", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := checkInstalled()
		if err == nil {
			t.Fatal("expected an error when Signal Desktop is not installed")
		}
		if !strings.Contains(err.Error(), "isn't installed") {
			t.Errorf("should say the app isn't installed, got %q", err)
		}
	})

	t.Run("app present but never linked", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, "Library", "Application Support", "Signal"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := checkInstalled()
		if err == nil {
			t.Fatal("expected an error when the message database is missing")
		}
		// Installed-but-empty needs different advice than not-installed;
		// conflating them sends the user to reinstall an app they already have.
		if strings.Contains(err.Error(), "isn't installed") {
			t.Errorf("installed-but-unlinked misreported as not installed: %q", err)
		}
		if !strings.Contains(err.Error(), "link your phone") {
			t.Errorf("should tell the user to link their phone, got %q", err)
		}
	})

	t.Run("fully set up", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		sql := filepath.Join(home, "Library", "Application Support", "Signal", "sql")
		if err := os.MkdirAll(sql, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sql, "db.sqlite"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkInstalled(); err != nil {
			t.Errorf("a complete install should pass the check, got %v", err)
		}
	})
}
