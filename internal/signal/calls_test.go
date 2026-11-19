package signal

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func logLine(at, msg string) string {
	b, _ := json.Marshal(map[string]any{"level": 30, "time": at, "msg": msg})
	return string(b) + "\n"
}

// Synthetic lines in the shape Signal Desktop logs; the ids are made up.
func TestCallLog(t *testing.T) {
	const day = "2026-01-02T"
	log := logLine(day+"10:00:00.000Z", "[calling] doGroupCallPeek/groupv2([REDACTED]aaa=): Found 3 devices") +
		logLine(day+"10:00:01.000Z", "[calling] doGroupCallPeek/groupv2([REDACTED]bbb=): Found 2 devices") +
		logLine(day+"10:00:02.000Z", "[calling] groupCallStateChange: [REDACTED]c0d Connecting NotJoined joined={} left={} ") +
		"not json at all [calling] \n" +
		logLine(day+"10:01:00.000Z", "[calling] doGroupCallPeek/groupv2([REDACTED]bbb=): Found 0 devices") +
		logLine(day+"10:01:00.000Z", "[other] groupCallStateChange: [REDACTED]c0d Connected Joined")

	l := &callLog{}
	if err := l.parse(strings.NewReader(log)); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, day+"10:02:00Z")

	if got := l.liveTails(now); len(got) != 1 || got[0] != "aaa=" {
		t.Errorf("live = %v, want only aaa= (bbb= dropped to 0 devices)", got)
	}
	if got := l.liveTails(now.Add(peekFresh)); len(got) != 0 {
		t.Errorf("live = %v, want none once the last check is stale", got)
	}
	if l.desktopState() != desktopLobby || l.desktopTail != "c0d" {
		t.Errorf("desktop = %s/%s, want lobby/c0d", l.desktopState(), l.desktopTail)
	}

	for _, c := range []struct{ line, want string }{
		{"[calling] groupCallStateChange: [REDACTED]c0d Connecting Joining joined={} left={} ", desktopJoined},
		{"[calling] groupCallStateChange: [REDACTED]c0d Connected Joined joined={} left={} ", desktopJoined},
		{"[calling] groupCallStateChange: [REDACTED]c0d Connected NotJoined joined={} left={} ", desktopLobby},
		{"[calling] groupCallStateChange: [REDACTED]c0d NotConnected NotJoined joined={} left={} ", desktopNone},
	} {
		if err := l.parse(strings.NewReader(logLine(day+"10:03:00.000Z", c.line))); err != nil {
			t.Fatal(err)
		}
		if got := l.desktopState(); got != c.want {
			t.Errorf("after %q: desktop = %s, want %s", c.line, got, c.want)
		}
	}
	if (&callLog{}).desktopState() != desktopNone {
		t.Error("an empty log should read as not on a call")
	}
}

func TestToggle(t *testing.T) {
	if want, press := toggle(false, "Mute mic", "Unmute mic"); want != "Unmute mic" || press != "Mute mic" {
		t.Errorf("mic off: want %q press %q", want, press)
	}
	if want, press := toggle(true, "Mute mic", "Unmute mic"); want != "Mute mic" || press != "Unmute mic" {
		t.Errorf("mic on: want %q press %q", want, press)
	}
}

// Dev hook against the real Signal Desktop, until calls have a CLI surface:
//
//	LURK_LIVE_CALLS=status       go test ./internal/signal -run TestLiveCalls -v
//	LURK_LIVE_CALLS=join:<conv>  …  (joins muted, camera off)
//	LURK_LIVE_CALLS=leave        …
//	LURK_LIVE_CALLS=ui           …  (can lurk read Signal's buttons? presses nothing)
func TestLiveCalls(t *testing.T) {
	op, arg, _ := strings.Cut(os.Getenv("LURK_LIVE_CALLS"), ":")
	if op == "" {
		t.Skip("set LURK_LIVE_CALLS=status|join:<conv>|leave to run against the real Signal Desktop")
	}
	if op == "ui" { // reads Signal's buttons, presses nothing
		start := time.Now()
		ui, err := openSignalUI()
		if err != nil {
			t.Fatal(err)
		}
		b, err := ui.list()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d buttons in %s; Join=%d Leave=%d", len(b), time.Since(start).Round(time.Millisecond), b.count("Join"), b.count("Leave"))
		return
	}
	if op == "leave" {
		if err := leaveCall(); err != nil {
			t.Fatal(err)
		}
		t.Log("left")
		return
	}
	db, cleanup, err := openSignalDB()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	switch op {
	case "status":
		st, err := getCallStatus(db, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.MarshalIndent(st, "", "  ")
		t.Logf("%s", out)
	case "join":
		start := time.Now()
		if err := joinCall(db, arg, false, false); err != nil {
			t.Fatal(err)
		}
		t.Logf("joined in %s", time.Since(start).Round(time.Millisecond))
	default:
		t.Fatalf("unknown LURK_LIVE_CALLS op %q", op)
	}
}
