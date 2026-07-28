package signal

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// addConv inserts a bare conversation (no seed message, unlike addConversation).
func addConv(t *testing.T, db *sqliteDB, id, name string) {
	t.Helper()
	if err := db.exec(fmt.Sprintf(
		`INSERT INTO conversations (id, type, name, active_at)
		 VALUES ('%s', 'private', '%s', %d)`, id, name, time.Now().UnixMilli())); err != nil {
		t.Fatal(err)
	}
}

// addMsg appends one message to a conversation. Rows are inserted in call order,
// so their hidden rowids climb in the same order — which is exactly the
// adjacency `search` context and `history --before` rely on.
func addMsg(t *testing.T, db *sqliteDB, msgID, convID, body string, at time.Time, typ string) {
	t.Helper()
	ms := at.UnixMilli()
	if err := db.exec(fmt.Sprintf(
		`INSERT INTO messages (id, conversationId, body, sent_at, received_at, type)
		 VALUES ('%s', '%s', '%s', %d, %d, '%s')`, msgID, convID, body, ms, ms, typ)); err != nil {
		t.Fatal(err)
	}
}

// A six-message conversation with the hit in the middle: -B1 -A2 should show the
// one message before and the two after, and nothing beyond that window.
func TestSearchContextWindow(t *testing.T) {
	db := newTestDB(t)
	addConv(t, db, "c1", "Alice")
	base := time.Now().Add(-time.Hour)
	bodies := []string{"one", "two", "three FINDME here", "four", "five", "six"}
	for i, b := range bodies {
		addMsg(t, db, fmt.Sprintf("m%d", i), "c1", b, base.Add(time.Duration(i)*time.Minute), "incoming")
	}

	out := captureStdout(t, func() {
		if err := cmdSearch(db, false, "FINDME", "", 25, 1, 2); err != nil {
			t.Fatal(err)
		}
	})

	for _, want := range []string{"two", "three FINDME here", "four", "five"} {
		if !strings.Contains(out, want) {
			t.Errorf("context window should include %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "one") {
		t.Errorf("before=1 must not reach 'one':\n%s", out)
	}
	if strings.Contains(out, "six") {
		t.Errorf("after=2 must not reach 'six':\n%s", out)
	}
	// The hit is marked and the surrounding lines are not.
	if !strings.Contains(out, "> [") {
		t.Errorf("the matched line should be marked with '>':\n%s", out)
	}
}

// A hit at the very start of a conversation just gets a shorter window rather
// than erroring or reaching into another conversation.
func TestSearchContextAtBoundary(t *testing.T) {
	db := newTestDB(t)
	addConv(t, db, "c1", "Alice")
	base := time.Now().Add(-time.Hour)
	addMsg(t, db, "m0", "c1", "opening FINDME", base, "incoming")
	addMsg(t, db, "m1", "c1", "reply after", base.Add(time.Minute), "incoming")
	// A different conversation whose messages must never bleed into the window.
	addConv(t, db, "c2", "Bob")
	addMsg(t, db, "n0", "c2", "unrelated chatter", base.Add(2*time.Minute), "incoming")

	out := captureStdout(t, func() {
		if err := cmdSearch(db, false, "FINDME", "", 25, 3, 1); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "reply after") {
		t.Errorf("after context should include the next message:\n%s", out)
	}
	if strings.Contains(out, "unrelated chatter") {
		t.Errorf("context must stay within the hit's conversation:\n%s", out)
	}
}

// Without -A/-B/-C the output is the flat, cross-conversation form: one line per
// hit naming its own conversation, no grouping header, no '>' marker.
func TestSearchNoContextStaysFlat(t *testing.T) {
	db := newTestDB(t)
	addConv(t, db, "c1", "Alice")
	addMsg(t, db, "m0", "c1", "just the FINDME line", time.Now().Add(-time.Hour), "incoming")

	out := captureStdout(t, func() {
		if err := cmdSearch(db, false, "FINDME", "", 25, 0, 0); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "→ Alice:") {
		t.Errorf("flat form should name the conversation inline:\n%s", out)
	}
	if strings.Contains(out, "> [") || strings.Contains(out, "# Alice") {
		t.Errorf("no-context form should not group or mark:\n%s", out)
	}
}

func TestContextLines(t *testing.T) {
	cases := []struct {
		name                  string
		ctxS, beforeS, afterS string
		wantBefore, wantAfter int
	}{
		{"none", "", "", "", 0, 0},
		{"context sets both", "3", "", "", 3, 3},
		{"after only", "", "", "2", 0, 2},
		{"before only", "", "4", "", 4, 0},
		{"sides override context", "3", "1", "5", 1, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, a := contextLines(c.ctxS, c.beforeS, c.afterS)
			if b != c.wantBefore || a != c.wantAfter {
				t.Errorf("contextLines(%q,%q,%q) = (%d,%d), want (%d,%d)",
					c.ctxS, c.beforeS, c.afterS, b, a, c.wantBefore, c.wantAfter)
			}
		})
	}
}

func TestPopFlagsMatchesShortAndLong(t *testing.T) {
	// Short spelling.
	v, rest := popFlags([]string{"-A", "3", "query"}, true, "-A", "--after-context")
	if v != "3" || len(rest) != 1 || rest[0] != "query" {
		t.Errorf("short flag: got v=%q rest=%v", v, rest)
	}
	// Long spelling, same call.
	v, rest = popFlags([]string{"hello", "--after-context", "5"}, true, "-A", "--after-context")
	if v != "5" || len(rest) != 1 || rest[0] != "hello" {
		t.Errorf("long flag: got v=%q rest=%v", v, rest)
	}
	// Absent leaves args untouched.
	v, rest = popFlags([]string{"just", "words"}, true, "-A", "--after-context")
	if v != "" || len(rest) != 2 {
		t.Errorf("absent flag: got v=%q rest=%v", v, rest)
	}
}
