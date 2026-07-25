package signal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestDB builds a real SQLCipher database with enough of Signal's schema for
// the summary queries. Going through the actual cgo wrapper — rather than faking
// rows — is deliberate: the --hours bug lived in summaryRows' SQL and bucketing,
// which a fake would have stepped straight over.
func newTestDB(t *testing.T) *sqliteDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A zero-length file is a valid empty SQLite database; SQLCipher encrypts it
	// on first write, so keying then creating tables gives us a real encrypted DB.
	db, err := openSQLCipher(path, strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("creating the test database: %v", err)
	}
	t.Cleanup(db.Close)

	if err := db.exec(`
		CREATE TABLE conversations (
			id TEXT PRIMARY KEY, type TEXT, name TEXT, profileFullName TEXT,
			profileName TEXT, e164 TEXT, active_at INTEGER, json TEXT);
		CREATE TABLE messages (
			id TEXT PRIMARY KEY, conversationId TEXT, body TEXT,
			sent_at INTEGER, received_at INTEGER, type TEXT);
	`); err != nil {
		t.Fatalf("creating the schema: %v", err)
	}
	return db
}

// addConversation inserts a conversation and one message at the given time.
func addConversation(t *testing.T, db *sqliteDB, id, name string, unread int, at time.Time, body string) {
	t.Helper()
	ms := at.UnixMilli()
	if err := db.exec(fmt.Sprintf(
		`INSERT INTO conversations (id, type, name, active_at, json)
		 VALUES ('%s', 'group', '%s', %d, '{"unreadCount":%d}')`, id, name, ms, unread)); err != nil {
		t.Fatal(err)
	}
	if err := db.exec(fmt.Sprintf(
		`INSERT INTO messages (id, conversationId, body, sent_at, received_at, type)
		 VALUES ('m-%s', '%s', '%s', %d, %d, 'incoming')`, id, id, body, ms, ms)); err != nil {
		t.Fatal(err)
	}
}

// captureStdout runs fn with os.Stdout redirected, returning what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

// The regression that motivated this: `--json signal summary` dumped raw DB rows,
// so it carried last_body for every recently-active conversation while the text
// form deliberately showed none. The machine-readable path leaked more than the
// human one. Assert against the real command, not the helper it delegates to.
func TestSummaryJSONDoesNotExposeBodiesTheTextFormWithholds(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	addConversation(t, db, "c-read", "Read Group", 0, now.Add(-10*time.Minute), "SECRET-RECENT-BODY")
	addConversation(t, db, "c-unread", "Unread Group", 3, now.Add(-20*time.Minute), "quoted on purpose")

	jsonOut := captureStdout(t, func() {
		if err := cmdSummary(db, true, 24); err != nil {
			t.Errorf("json summary: %v", err)
		}
	})
	textOut := captureStdout(t, func() {
		if err := cmdSummary(db, false, 24); err != nil {
			t.Errorf("text summary: %v", err)
		}
	})

	if strings.Contains(jsonOut, "SECRET-RECENT-BODY") {
		t.Errorf("--json exposed the body of a merely-recent conversation:\n%s", jsonOut)
	}
	if strings.Contains(textOut, "SECRET-RECENT-BODY") {
		t.Errorf("text form exposed the body of a merely-recent conversation:\n%s", textOut)
	}
	// Both forms should still surface the unread conversation and its message.
	for name, out := range map[string]string{"json": jsonOut, "text": textOut} {
		if !strings.Contains(out, "Unread Group") {
			t.Errorf("%s form dropped the unread conversation:\n%s", name, out)
		}
		if !strings.Contains(out, "quoted on purpose") {
			t.Errorf("%s form dropped the unread conversation's message:\n%s", name, out)
		}
		if !strings.Contains(out, "Read Group") {
			t.Errorf("%s form dropped the recently-active conversation:\n%s", name, out)
		}
	}

	// And the JSON must be digest items — the shape `lurk summary --json` emits —
	// rather than the old {"unread": [...], "recent": [...]} row dump.
	var items []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &items); err != nil {
		t.Fatalf("--json should emit an array of digest items, got %v\n%s", err, jsonOut)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 digest items, got %d: %s", len(items), jsonOut)
	}
	for _, it := range items {
		if _, ok := it["last_body"]; ok {
			t.Errorf("raw DB column last_body leaked into the JSON output: %v", it)
		}
	}
}

// --hours bounds recent activity only; unread is a state and is always listed.
// Exercised through the real SQL, since that is where the bucketing lives.
func TestHoursWindowsActivityButNotUnread(t *testing.T) {
	db := newTestDB(t)
	now := time.Now()
	addConversation(t, db, "c-old-unread", "Old Unread", 1, now.Add(-90*24*time.Hour), "still waiting")
	addConversation(t, db, "c-old-read", "Old Read", 0, now.Add(-90*24*time.Hour), "long done")
	addConversation(t, db, "c-new-read", "New Read", 0, now.Add(-10*time.Minute), "just chatter")

	unread, recent, err := summaryRows(db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) != 1 || asStr(unread[0]["name"]) != "Old Unread" {
		t.Errorf("an unread conversation must survive --hours 1 however old it is, got %v", unread)
	}
	if len(recent) != 1 || asStr(recent[0]["name"]) != "New Read" {
		t.Errorf("--hours 1 should keep only activity inside the window, got %v", recent)
	}

	out := captureStdout(t, func() {
		if err := cmdSummary(db, false, 1); err != nil {
			t.Errorf("summary: %v", err)
		}
	})
	if !strings.Contains(out, "outside the window") {
		t.Errorf("a 90-day-old unread should be marked as outside the window:\n%s", out)
	}
	if strings.Contains(out, "Old Read") {
		t.Errorf("a 90-day-old *read* conversation should not appear under --hours 1:\n%s", out)
	}
}
