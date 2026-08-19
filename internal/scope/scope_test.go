package scope

import (
	"strings"
	"testing"
)

const sample = `
# work only
slack Acme Corp
slack widgets/#eng
slack widgets/#eng-oncall
signal Team Chat
signal +15551234567
`

func parseOrFail(t *testing.T, s string) *Scope {
	t.Helper()
	sc, err := parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return sc
}

func TestSlackWorkspaceAndChannels(t *testing.T) {
	s := parseOrFail(t, sample)

	// A bare workspace line admits every channel in it.
	chans, ok := s.SlackWorkspace("Acme Corp", "T111", "https://acme.slack.com/")
	if !ok || chans != nil {
		t.Fatalf("bare workspace line should admit all channels; got %v %v", chans, ok)
	}

	// Matched by subdomain, and narrowed to exactly the named channels.
	chans, ok = s.SlackWorkspace("Widgets Inc", "T222", "https://widgets.slack.com/")
	if !ok {
		t.Fatal("workspace should match on its slack.com subdomain")
	}
	for _, tc := range []struct {
		id, name string
		want     bool
	}{
		{"C1", "eng", true},
		{"C1", "#eng", true},         // leading # is noise either side
		{"C2", "ENG", true},          // case-insensitive
		{"C3", "engineering", false}, // not a prefix match
		{"C4", "random", false},
	} {
		if got := SlackChannel(chans, tc.id, tc.name); got != tc.want {
			t.Errorf("SlackChannel(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// An ID written straight into the config matches without a name.
	if !SlackChannel(map[string]bool{"c9xyz": true}, "C9XYZ", "") {
		t.Error("a channel ID in the config should match by ID alone")
	}

	// A workspace the file never names is refused outright.
	if _, ok := s.SlackWorkspace("Book Club", "T333", "https://bookclub.slack.com/"); ok {
		t.Error("an unnamed workspace must not be readable")
	}
}

func TestSignalMatching(t *testing.T) {
	s := parseOrFail(t, sample)
	for _, tc := range []struct {
		id, name, e164 string
		want           bool
	}{
		{"c-1", "Team Chat", "", true},
		{"c-1", "team chat", "", true},
		{"c-2", "Alice", "+15551234567", true},
		{"c-3", "Team Chat Overflow", "", false}, // exact, not substring
		{"c-4", "Book Club", "", false},
	} {
		if got := s.Signal(tc.id, tc.name, tc.e164); got != tc.want {
			t.Errorf("Signal(%q,%q) = %v, want %v", tc.name, tc.e164, got, tc.want)
		}
	}
}

func TestNilScopeAdmitsEverything(t *testing.T) {
	var s *Scope
	if _, ok := s.SlackWorkspace("Anything", "T0", ""); !ok {
		t.Error("no config must mean no restriction")
	}
	if !s.Signal("id", "name", "") {
		t.Error("no config must mean no restriction")
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{
		"telegram Foo",   // unknown source
		"slack",          // names nothing
		"slack widgets/", // no channel after the slash
	} {
		if _, err := parse(strings.NewReader(bad)); err == nil {
			t.Errorf("parse(%q) should have failed", bad)
		}
	}
}
