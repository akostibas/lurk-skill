package slack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// The Mac App Store build keeps its data in a sandbox container; a Mac that
// switched builds has both folders, and only the newer session is live.
func TestSlackSupportPicksNewestCookies(t *testing.T) {
	setup := func(t *testing.T, ages map[int]time.Duration) []string {
		t.Setenv("HOME", t.TempDir())
		dirs := slackDirs()
		for i, age := range ages {
			if err := os.MkdirAll(dirs[i], 0o755); err != nil {
				t.Fatal(err)
			}
			c := filepath.Join(dirs[i], "Cookies")
			if err := os.WriteFile(c, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			mt := time.Now().Add(-age)
			if err := os.Chtimes(c, mt, mt); err != nil {
				t.Fatal(err)
			}
		}
		return dirs
	}

	t.Run("app store only", func(t *testing.T) {
		dirs := setup(t, map[int]time.Duration{1: 0})
		if got := slackSupport(); got != dirs[1] {
			t.Errorf("got %s, want the App Store container", got)
		}
		if err := checkInstalled(); err != nil {
			t.Errorf("App Store install should pass the check, got %v", err)
		}
	})
	t.Run("direct only", func(t *testing.T) {
		dirs := setup(t, map[int]time.Duration{0: 0})
		if got := slackSupport(); got != dirs[0] {
			t.Errorf("got %s, want the direct-download folder", got)
		}
	})
	t.Run("both, app store newer", func(t *testing.T) {
		dirs := setup(t, map[int]time.Duration{0: time.Hour, 1: 0})
		if got := slackSupport(); got != dirs[1] {
			t.Errorf("got %s, want the newer App Store folder", got)
		}
	})
	t.Run("both, direct newer", func(t *testing.T) {
		dirs := setup(t, map[int]time.Duration{0: 0, 1: time.Hour})
		if got := slackSupport(); got != dirs[0] {
			t.Errorf("got %s, want the newer direct-download folder", got)
		}
	})
	t.Run("neither names both paths", func(t *testing.T) {
		dirs := setup(t, nil)
		err := checkInstalled()
		if err == nil || !strings.Contains(err.Error(), dirs[0]) || !strings.Contains(err.Error(), dirs[1]) {
			t.Errorf("error should name both folders, got %v", err)
		}
	})
}
