package slack

import (
	"testing"
)

func TestNormalizeTS(t *testing.T) {
	cases := []struct{ in, want string }{
		// The p-form permalink id: strip the p, split the trailing 6 micros.
		{"p1785126651515049", "1785126651.515049"},
		{"1785126651515049", "1785126651.515049"},
		// Already decimal — untouched.
		{"1785126651.515049", "1785126651.515049"},
		{"p1785126651.515049", "1785126651.515049"},
		// Not a ts we can split — passes through rather than corrupting.
		{"", ""},
		{"abc", "abc"},
	}
	for _, c := range cases {
		if got := normalizeTS(c.in); got != c.want {
			t.Errorf("normalizeTS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParsePermalink(t *testing.T) {
	// thread_ts query param is the true root and wins over the path p-id.
	host, ch, ts, ok := parsePermalink("https://sway.slack.com/archives/C08JW756EUT/p1785126651515049?thread_ts=1785120000.000100")
	if !ok || host != "sway.slack.com" || ch != "C08JW756EUT" || ts != "1785120000.000100" {
		t.Fatalf("query-param root: got host=%q ch=%q ts=%q ok=%v", host, ch, ts, ok)
	}

	// No query param: fall back to the path p-id, normalized to decimal.
	_, ch, ts, ok = parsePermalink("https://sway.slack.com/archives/C08JW756EUT/p1785126651515049")
	if !ok || ch != "C08JW756EUT" || ts != "1785126651.515049" {
		t.Fatalf("path-id root: got ch=%q ts=%q ok=%v", ch, ts, ok)
	}

	// Non-message URLs are rejected rather than half-parsed.
	if _, _, _, ok := parsePermalink("https://sway.slack.com/team/U123"); ok {
		t.Error("a non-archives URL should not parse as a thread locator")
	}
	if _, _, _, ok := parsePermalink("not a url at all"); ok {
		t.Error("a non-URL should not parse as a thread locator")
	}
}

func TestResolveRepliesTarget(t *testing.T) {
	// Legacy triple, with a pasted p-form ts normalized in passing.
	ws, ch, ts, err := resolveRepliesTarget([]string{"Sway", "C08", "p1785126651515049"})
	if err != nil || ws != "Sway" || ch != "C08" || ts != "1785126651.515049" {
		t.Fatalf("legacy triple: ws=%q ch=%q ts=%q err=%v", ws, ch, ts, err)
	}

	// A permalink resolves without any cache state.
	ws, ch, ts, err = resolveRepliesTarget([]string{"https://sway.slack.com/archives/C08/p1785126651515049"})
	if err != nil || ws != "sway.slack.com" || ch != "C08" || ts != "1785126651.515049" {
		t.Fatalf("permalink: ws=%q ch=%q ts=%q err=%v", ws, ch, ts, err)
	}

	// A code with no matching cache entry is a clear error, not a silent miss.
	if _, _, _, err := resolveRepliesTarget([]string{"999999"}); err == nil {
		t.Error("an unknown code should error")
	}
	// Two args is neither shape.
	if _, _, _, err := resolveRepliesTarget([]string{"a", "b"}); err == nil {
		t.Error("two positionals should be rejected")
	}
}

func TestCodeAssignRoundTrip(t *testing.T) {
	// Isolate the cache in a temp dir + fixed session so the test is hermetic.
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "test-session")

	locA := locator{Workspace: "Sway", Channel: "C1", ThreadTS: "111.000001", Permalink: "https://x/a"}
	locB := locator{Workspace: "Sway", Channel: "C2", ThreadTS: "222.000002"}

	a := assignCode(locA)
	b := assignCode(locB)
	if a != 1 || b != 2 {
		t.Fatalf("codes should climb from 1: got a=%d b=%d", a, b)
	}
	// Same thread keeps its code — the counter doesn't churn on re-listing.
	if again := assignCode(locA); again != a {
		t.Errorf("re-assigning the same thread changed its code: %d != %d", again, a)
	}

	got, ok := resolveCode(a)
	if !ok || got.Channel != "C1" || got.ThreadTS != "111.000001" {
		t.Errorf("resolveCode(%d) = %+v ok=%v", a, got, ok)
	}
	if _, ok := resolveCode(999); ok {
		t.Error("resolveCode of an unassigned code should miss")
	}
}

func TestAssignCodeSkipsIncompleteLocator(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_SESSION_ID", "test-session-2")
	if code := assignCode(locator{Workspace: "Sway", Channel: "C1"}); code != 0 {
		t.Errorf("a locator with no thread_ts should get no code, got %d", code)
	}
}

func TestSessionsDoNotShareCodes(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-one")
	assignCode(locator{Workspace: "W", Channel: "C1", ThreadTS: "1.1"})

	// A different session starts its own numbering and can't see the first's code.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-two")
	if code := assignCode(locator{Workspace: "W", Channel: "C9", ThreadTS: "9.9"}); code != 1 {
		t.Errorf("a fresh session should start at 1, got %d", code)
	}
	if _, ok := resolveCode(1); ok {
		loc, _ := resolveCode(1)
		if loc.Channel == "C1" {
			t.Error("session-two resolved session-one's thread — sessions must not share codes")
		}
	}
}
