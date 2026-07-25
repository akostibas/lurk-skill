package signal

import (
	"strings"
	"testing"
	"time"

	"github.com/akostibas/lurk-skill/internal/digest"
)

func row(name string, unread int, markedUnread int, lastAt int64, lastBody string) map[string]any {
	return map[string]any{
		"name":         name,
		"unread":       int64(unread),
		"markedUnread": int64(markedUnread),
		"last_at":      lastAt,
		"last_body":    lastBody,
	}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// The digest lists unread conversations however old they are — that's the point
// of the tool. But an item sitting above this morning's activity because of a
// state, not an event, has to say so, or `--hours 1` looks broken.
func TestUnreadOutsideTheWindowIsMarked(t *testing.T) {
	now := time.Now()
	since := ms(now.Add(-1 * time.Hour))

	unread := []map[string]any{
		row("Stale Thread", 1, 0, ms(now.Add(-90*24*time.Hour)), "months ago"),
		row("Fresh Thread", 2, 0, ms(now.Add(-10*time.Minute)), "just now"),
	}
	items := itemsFrom(unread, nil, since)

	if len(items) != 2 {
		t.Fatalf("expected both unread conversations, got %d", len(items))
	}
	if !strings.Contains(items[0].Note, "outside the window") {
		t.Errorf("a 90-day-old unread should be marked as outside the window, got note %q", items[0].Note)
	}
	if strings.Contains(items[1].Note, "outside the window") {
		t.Errorf("an unread from 10 minutes ago is inside the window, got note %q", items[1].Note)
	}
	if !strings.Contains(items[1].Note, "2 unread") {
		t.Errorf("the unread count should survive the marking, got note %q", items[1].Note)
	}
}

// A conversation with no messages at all has last_at 0, which is "before" any
// cutoff by arithmetic but isn't a stale conversation — don't label it.
func TestUnreadWithNoTimestampIsNotMarkedStale(t *testing.T) {
	items := itemsFrom([]map[string]any{row("Empty", 0, 1, 0, "")}, nil, ms(time.Now()))
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	if strings.Contains(items[0].Note, "outside the window") {
		t.Errorf("a conversation with no messages should not be called stale, got note %q", items[0].Note)
	}
	if items[0].Note != "marked unread" {
		t.Errorf("expected the marked-unread note, got %q", items[0].Note)
	}
}

// `--json signal summary` used to dump the raw DB rows, which carried last_body
// for merely-recent conversations that the text form deliberately left blank.
// Both forms now come from itemsFrom, so this asserts the field set directly:
// nothing recent may carry message text, whichever way it is rendered.
func TestRecentActivityCarriesNoMessageBody(t *testing.T) {
	now := time.Now()
	recent := []map[string]any{
		row("Chatty Group", 0, 0, ms(now.Add(-5*time.Minute)), "a private message body"),
	}
	items := itemsFrom(nil, recent, ms(now.Add(-time.Hour)))

	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	if items[0].Kind != digest.Recent {
		t.Fatalf("expected a recent item, got kind %q", items[0].Kind)
	}
	if items[0].Text != "" {
		t.Errorf("recent activity must not expose a message body, got %q", items[0].Text)
	}
	// Belt and braces: whatever fields exist, none of them may hold the body.
	for _, got := range []string{items[0].Text, items[0].Note, items[0].Who, items[0].Where} {
		if strings.Contains(got, "a private message body") {
			t.Errorf("message body leaked into a rendered field: %q", got)
		}
	}
}

// Unread items do quote the last message — that's what makes the digest useful
// — so guard the boundary rather than only the negative case above.
func TestUnreadDoesQuoteTheLastMessage(t *testing.T) {
	items := itemsFrom([]map[string]any{row("Alice", 1, 0, ms(time.Now()), "are we still on for 3?")}, nil, 0)
	if len(items) != 1 || items[0].Text != "are we still on for 3?" {
		t.Fatalf("unread items should quote the last message, got %+v", items)
	}
}
