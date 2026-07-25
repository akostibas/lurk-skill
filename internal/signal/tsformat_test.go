package signal

import (
	"regexp"
	"testing"
	"time"
)

// linePrefix is how downstream callers actually find the timestamp: a bracketed
// prefix at the start of a text-mode line. Written as the consumer's regex
// rather than as our layout string, so the test fails for the reason they'd
// break rather than merely noticing the constant changed.
var linePrefix = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2})\]`)

// The text-mode timestamp is an output contract. A shift in it doesn't raise an
// error anywhere — a caller's window filter just stops matching, and an hourly
// digest reports a quiet day at a client that was not, in fact, quiet. Pin it.
func TestHistoryTimestampPrefixIsStable(t *testing.T) {
	when := time.Date(2026, 3, 14, 9, 2, 0, 0, time.Local)

	got := fmtTime(when.UnixMilli())
	if got != "2026-03-14 09:02" {
		t.Errorf("signal text-mode timestamp changed: got %q, want %q — see tsLayout", got, "2026-03-14 09:02")
	}
	if !linePrefix.MatchString("[" + got + "] someone: hello") {
		t.Errorf("a history line built from fmtTime no longer parses as [YYYY-MM-DD HH:MM]: %q", got)
	}
}

// Zero means "no timestamp", not "the epoch" — a line with an epoch date would
// silently land inside nobody's window and outside everybody's expectations.
func TestNoTimestampRendersEmpty(t *testing.T) {
	if got := fmtTime(0); got != "" {
		t.Errorf("a missing timestamp should render empty, got %q", got)
	}
}
