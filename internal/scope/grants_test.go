package scope

import (
	"os"
	"path/filepath"
	"testing"
)

// A signal-calls line grants acting, not reading: on its own it must leave
// reading unbounded, and a read line must never imply it.
func TestCallGrants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scope")
	t.Setenv("LURK_CONFIG", path)
	t.Setenv("LURK_REQUIRE_SCOPE", "")
	t.Cleanup(func() { cur, grants = nil, nil })
	load := func(body string) error {
		t.Helper()
		cur, grants = nil, nil
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load()
	}

	cur, grants = nil, nil
	if SignalCalls("id1", "Team Chat", "") {
		t.Error("with no config, calls must be denied")
	}

	if err := load("signal-calls Team Chat\n"); err != nil {
		t.Fatal(err)
	}
	if Active() {
		t.Error("a grants-only file must not bound reading")
	}
	if !SignalCalls("id1", "team chat", "") || SignalCalls("id2", "Other", "") {
		t.Error("the grant should cover exactly the named conversation")
	}
	if ConfigPath() != path {
		t.Errorf("ConfigPath = %q, want the file in force", ConfigPath())
	}

	if err := load("signal Team Chat\nsignal Other\nsignal-calls Other\n"); err != nil {
		t.Fatal(err)
	}
	if !Active() || SignalCalls("id1", "Team Chat", "") || !SignalCalls("id2", "Other", "") {
		t.Error("a read line must not grant calls; the signal-calls line must")
	}

	if err := load("# nothing named\n"); err != nil {
		t.Fatal(err)
	}
	if !Active() || Current().Signal("id1", "Team Chat", "") {
		t.Error("an empty file must still read nothing")
	}

	t.Setenv("LURK_REQUIRE_SCOPE", "1")
	if err := load("signal-calls Team Chat\n"); err == nil {
		t.Error("LURK_REQUIRE_SCOPE must reject a file that bounds no reading")
	}
}
