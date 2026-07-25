package digest

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// at builds a timestamp `ago` before now, so tests exercise the same
// relative-time formatting real runs do.
func at(ago time.Duration) time.Time { return time.Now().Add(-ago) }

func render(items []Item) string {
	var b bytes.Buffer
	Render(&b, items)
	return b.String()
}

func TestRenderGroupsBySourceAndScope(t *testing.T) {
	out := render([]Item{
		{Source: "slack", Scope: "Acme", Kind: Mention, Who: "dana", Where: "#eng", Text: "can you look"},
		{Source: "signal", Kind: Unread, Who: "Mom", Text: "call me", Note: "2 unread"},
		{Source: "slack", Scope: "Acme", Kind: Channel, Where: "#random"},
	})

	// Both sources appear, each under its own header, and the Slack workspace
	// is named while Signal (which has no scope) is not.
	for _, want := range []string{"═══ SLACK / Acme ═══", "═══ SIGNAL ═══", "#random", "call me"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Items from the same source+scope must stay in one block even when
	// interleaved in the input, or a digest reads as duplicated sections.
	if strings.Count(out, "═══ SLACK / Acme ═══") != 1 {
		t.Errorf("Slack group split across multiple blocks:\n%s", out)
	}
	if got := strings.Index(out, "SIGNAL"); got < strings.Index(out, "#random") {
		t.Errorf("Signal block should follow the complete Slack block:\n%s", out)
	}
}

func TestRenderOrdersSectionsByUrgency(t *testing.T) {
	// Deliberately supplied worst-first: ambient channel noise before mentions.
	out := render([]Item{
		{Source: "slack", Scope: "Acme", Kind: Channel, Where: "#random"},
		{Source: "slack", Scope: "Acme", Kind: Thread, Where: "#eng"},
		{Source: "slack", Scope: "Acme", Kind: Mention, Who: "dana", Text: "ping"},
	})
	mention, thread, channel := strings.Index(out, "Mentions"), strings.Index(out, "Active threads"), strings.Index(out, "Other unread")
	if !(mention < thread && thread < channel) {
		t.Errorf("sections should run mentions → threads → channels, got %d/%d/%d:\n%s",
			mention, thread, channel, out)
	}
}

func TestRenderSkipsEmptySections(t *testing.T) {
	out := render([]Item{{Source: "signal", Kind: Unread, Who: "Mom", Text: "hi"}})
	if strings.Contains(out, "Mentions") || strings.Contains(out, "Active threads") {
		t.Errorf("sections with no items should be omitted entirely:\n%s", out)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := render(nil); !strings.Contains(got, "Nothing waiting") {
		t.Errorf("an empty digest should say so plainly, got %q", got)
	}
}

func TestLineOmitsMissingParts(t *testing.T) {
	cases := []struct {
		name string
		item Item
		want string
	}{
		{"who and text", Item{Who: "dana", Text: "hello"}, "dana: hello"},
		// A name with nothing quoted must not trail a colon.
		{"who only", Item{Who: "Mom"}, "Mom"},
		{"where and note", Item{Where: "#eng", Note: "2 mentions"}, "#eng  (2 mentions)"},
		{"where who text", Item{Where: "#eng", Who: "dana", Text: "hi"}, "#eng  dana: hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.item.line(); got != c.want {
				t.Errorf("line() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestLineTimeFormatDependsOnAge(t *testing.T) {
	recent := Item{When: at(2 * time.Hour), Who: "dana", Text: "hi"}.line()
	if strings.Contains(recent, "-") { // no date component
		t.Errorf("recent items should show weekday+time, got %q", recent)
	}

	// Past a week, a weekday alone is ambiguous — the date has to appear.
	old := Item{When: at(20 * 24 * time.Hour), Who: "dana", Text: "hi"}.line()
	if !strings.Contains(old, time.Now().Add(-20*24*time.Hour).Format("2006-01-02")) {
		t.Errorf("items older than a week should show the date, got %q", old)
	}
}

func TestLinkRendersUnderItsItem(t *testing.T) {
	out := render([]Item{
		{Source: "slack", Scope: "Acme", Kind: Mention, Who: "dana", Text: "ping", Link: "https://example.slack.com/p1"},
		{Source: "slack", Scope: "Acme", Kind: Mention, Who: "omar", Text: "pong"},
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, l := range lines {
		if strings.Contains(l, "https://example.slack.com/p1") {
			if i == 0 || !strings.Contains(lines[i-1], "dana") {
				t.Errorf("permalink should sit directly under its own item:\n%s", out)
			}
			return
		}
	}
	t.Errorf("permalink missing from output:\n%s", out)
}
