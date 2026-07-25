package slack

import (
	"regexp"
	"strconv"
	"testing"
	"time"
)

// See the matching test in internal/signal: the bracketed timestamp prefix on
// text-mode lines is parsed by downstream callers, and a change to it degrades
// silently rather than failing. Both sources must keep the same layout.
var linePrefix = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2})\]`)

func TestSearchAndHistoryTimestampPrefixIsStable(t *testing.T) {
	when := time.Date(2026, 3, 14, 9, 2, 0, 0, time.Local)

	// Slack hands us "<unix>.<seq>"; the fractional part is a message ordinal,
	// not a sub-second time, and must not reach the rendered line.
	got := fmtTS(strconv.FormatInt(when.Unix(), 10) + ".004200")
	if got != "2026-03-14 09:02" {
		t.Errorf("slack text-mode timestamp changed: got %q, want %q — see tsLayout", got, "2026-03-14 09:02")
	}
	if !linePrefix.MatchString("[" + got + "] @someone: hello") {
		t.Errorf("a search/history line built from fmtTS no longer parses as [YYYY-MM-DD HH:MM]: %q", got)
	}
}

// An unparseable ts passes through verbatim rather than becoming a wrong date.
// A caller filtering on the prefix then skips the line, which is the safe
// failure; inventing a plausible timestamp would not be.
func TestUnparseableTimestampPassesThrough(t *testing.T) {
	if got := fmtTS("not-a-timestamp"); got != "not-a-timestamp" {
		t.Errorf("an unparseable ts should pass through unchanged, got %q", got)
	}
}
