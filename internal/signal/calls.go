package signal

// Group-call awareness and control for Signal Desktop. Detection is read-only:
// the DB never records a call ending, so liveness comes from Signal's own log.
// joinCall/leaveCall are the one place lurk acts, by pressing Signal's buttons.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// peekFresh bounds how old a device count may be and still mean "live". Signal
// re-checks a running call every 1–2 min; older means it quit or the Mac slept.
const peekFresh = 5 * time.Minute

// Desktop call states, from this Mac's Signal's point of view.
const (
	desktopNone   = "none"
	desktopLobby  = "lobby" // pre-join screen open, not yet in the call
	desktopJoined = "joined"
)

var (
	// Signal redacts ids in its log down to a short tail, which is what we match on.
	peekRe  = regexp.MustCompile(`^\[calling\] doGroupCallPeek/groupv2\(\[REDACTED\]([^)]+)\): Found (\d+) devices`)
	stateRe = regexp.MustCompile(`^\[calling\] groupCallStateChange: \[REDACTED\](\S+) (\w+) (\w+)`)
)

type peek struct {
	devices int
	at      time.Time
}

// callLog is what Signal's log says about calls: the last device count per
// group (keyed by groupId tail) and the desktop's own last connection state.
type callLog struct {
	peeks       map[string]peek
	desktopTail string // conversation-id tail of the desktop's last call
	conn, join  string
	desktopAt   time.Time
}

func (l *callLog) parse(r io.Reader) error {
	if l.peeks == nil {
		l.peeks = map[string]peek{}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte("[calling] ")) {
			continue
		}
		var e struct {
			Time time.Time `json:"time"`
			Msg  string    `json:"msg"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if m := peekRe.FindStringSubmatch(e.Msg); m != nil {
			n, _ := strconv.Atoi(m[2])
			l.peeks[m[1]] = peek{devices: n, at: e.Time}
		} else if m := stateRe.FindStringSubmatch(e.Msg); m != nil {
			l.desktopTail, l.conn, l.join, l.desktopAt = m[1], m[2], m[3], e.Time
		}
	}
	return sc.Err()
}

// desktopState reduces Signal's (connection, join) pair to none/lobby/joined.
// ponytail: a Signal crash mid-call leaves "joined" as the last word until the
// next call event; cross-check against the app's start time if that bites.
func (l *callLog) desktopState() string {
	switch {
	case l.join == "Joined" || l.join == "Joining" || l.join == "Pending":
		return desktopJoined
	case l.conn != "" && l.conn != "NotConnected":
		return desktopLobby
	}
	return desktopNone
}

// liveTails is the groupId tails whose last device count is non-zero and fresh.
func (l *callLog) liveTails(now time.Time) []string {
	var out []string
	for tail, p := range l.peeks {
		if p.devices > 0 && now.Sub(p.at) <= peekFresh {
			out = append(out, tail)
		}
	}
	sort.Strings(out)
	return out
}

// readCallLog parses Signal's rotated-out log and then the current one, so the
// newest line wins.
func readCallLog() (*callLog, error) {
	l := &callLog{}
	dir := filepath.Join(signalDir(), "logs")
	read := 0
	for _, name := range []string{"app.log.1", "app.log"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		err = l.parse(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("read Signal's %s: %w", name, err)
		}
		read++
	}
	if read == 0 {
		return nil, fmt.Errorf("Signal Desktop has no log in %s — has it run on this Mac?", dir)
	}
	return l, nil
}

// groupCall is a call Signal Desktop currently sees with someone in it.
type groupCall struct {
	ConvID  string    `json:"conv_id"`
	Name    string    `json:"name"`
	Devices int       `json:"devices"`
	SeenAt  time.Time `json:"seen_at"`
}

// callStatus is the read-only answer to "is a call on, and is this Mac in it?".
// Device counts say a call is live, not that the user is in it on another device.
type callStatus struct {
	Live        []groupCall `json:"live"`
	Desktop     string      `json:"desktop"` // none | lobby | joined
	DesktopConv string      `json:"desktop_conv,omitempty"`
	desktopID   string
}

// convByTail resolves a redacted log id to a group chat; nil when it is
// ambiguous, unknown, or outside the declared scope. Group-only because the
// conversation-id tail is just 3 hex chars and collides across all chats.
func convByTail(db *sqliteDB, col, tail string) *conv {
	rows, err := db.query(fmt.Sprintf(
		"SELECT id, type, %s AS name, e164 FROM conversations c WHERE c.type='group' AND c.%s LIKE '%%'||?",
		dispName("c"), col), tail)
	if err != nil {
		return nil
	}
	if rows = keepInScope(rows, "id", "name"); len(rows) != 1 {
		return nil
	}
	return rowConv(rows[0])
}

func getCallStatus(db *sqliteDB, now time.Time) (*callStatus, error) {
	l, err := readCallLog()
	if err != nil {
		return nil, err
	}
	st := &callStatus{Desktop: l.desktopState(), Live: []groupCall{}}
	for _, tail := range l.liveTails(now) {
		if c := convByTail(db, "groupId", tail); c != nil {
			p := l.peeks[tail]
			st.Live = append(st.Live, groupCall{ConvID: c.ID, Name: c.Name, Devices: p.devices, SeenAt: p.at})
		}
	}
	if st.Desktop != desktopNone {
		st.DesktopConv = "a chat lurk can't name"
		if c := convByTail(db, "id", l.desktopTail); c != nil {
			st.DesktopConv, st.desktopID = c.Name, c.ID
		}
	}
	// Signal stops logging device counts for a call the desktop itself is in,
	// so that call is live on the desktop's own say-so.
	if st.Desktop == desktopJoined && st.desktopID != "" {
		listed := false
		for _, g := range st.Live {
			listed = listed || g.ConvID == st.desktopID
		}
		if !listed {
			st.Live = append(st.Live, groupCall{ConvID: st.desktopID, Name: st.DesktopConv, Devices: 1, SeenAt: l.desktopAt})
		}
	}
	return st, nil
}

// Signal wraps names in Unicode isolates (U+2068 … U+2069) in button titles.
func isolated(name string) string { return "⁨" + name + "⁩" }

// toggle returns the title a lobby switch shows in the wanted state, and the
// title to press to get there from the other state.
func toggle(on bool, whenOn, whenOff string) (want, press string) {
	if on {
		return whenOn, whenOff
	}
	return whenOff, whenOn
}

// callOpts is how the desktop enters a call. The zero value is the quiet one:
// muted, camera off, and (when starting) nobody's phone rings.
type callOpts struct {
	mic, camera bool
	ring        bool // startCall only: ring the group's phones instead of just notifying
}

// joinCall joins the live group call in convArg from Signal Desktop. It refuses
// to disturb a call the desktop is already in, and never starts a call itself.
func joinCall(db *sqliteDB, convArg string, o callOpts) error {
	return enterCall(db, convArg, false, o)
}

// startCall starts a new group call in convArg. It refuses when one is already
// running there (join it instead) and when the desktop is busy with another.
func startCall(db *sqliteDB, convArg string, o callOpts) error {
	return enterCall(db, convArg, true, o)
}

// enterCall is the shared path. start is strict both ways: joining never starts
// a call and starting never joins one, so a wrong guess can't ring a group.
func enterCall(db *sqliteDB, convArg string, start bool, o callOpts) error {
	c, err := resolveConv(db, convArg)
	if err != nil {
		return err
	}
	st, err := getCallStatus(db, time.Now())
	if err != nil {
		return err
	}
	switch st.Desktop {
	case desktopJoined:
		if st.desktopID == c.ID {
			return fmt.Errorf("Signal Desktop is already on the call in %q — nothing to do", c.Name)
		}
		return fmt.Errorf("Signal Desktop is already on a call (%s) — leaving it undisturbed; leave that call first", st.DesktopConv)
	case desktopLobby:
		return fmt.Errorf("Signal Desktop already has a call lobby open (%s) — leaving it undisturbed", st.DesktopConv)
	}
	live := false
	for _, g := range st.Live {
		live = live || g.ConvID == c.ID
	}
	// header opens the lobby; confirm is the lobby's own button that enters the call.
	header, confirm := "Join", "Join"
	switch {
	case start && c.Type != "group":
		return fmt.Errorf("%q is not a group — lurk only starts group calls so far", c.Name)
	case start && live:
		return fmt.Errorf("a call is already running in %q — join it instead of starting another", c.Name)
	case start:
		header, confirm = "Start a video call", "Start"
	case !live:
		return fmt.Errorf("no live call in %q — joining never starts one; use start for that", c.Name)
	}

	ui, err := openSignalUI()
	if err != nil {
		return err
	}
	// The header's call button only exists for the chat that is open, so open it.
	// ponytail: only finds chats currently visible in the list (not scrolled away
	// or archived); a chat with a live call was just active, so it's near the top.
	if ok, err := ui.pressPrefix("Chat with " + isolated(c.Name) + ","); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("can't find %q in Signal's chat list — scroll it into view (or unarchive it) and retry", c.Name)
	}
	b, err := ui.waitFor(5*time.Second, func(b buttons) bool { return b.hasPrefix(isolated(c.Name)) && b.has(header) })
	if err != nil {
		return fmt.Errorf("opened %q but Signal shows no %q button — the call may have just started or ended", c.Name, header)
	}
	// Exact titles only, and never both: Join enters quietly, Start can ring a group.
	if b.count(header) != 1 || b.has("Leave") || (start && b.has("Join")) {
		return errors.New("Signal's window isn't in the expected state; not pressing anything")
	}
	if _, err := ui.press(header); err != nil {
		return err
	}
	hasMic := func(b buttons) bool { return b.has("Mute mic") || b.has("Unmute mic") }
	inLobby := func(b buttons) bool { return b.has("Cancel") && b.count(confirm) == 1 && hasMic(b) }
	// cancel backs out of the lobby, but only if a lobby is what's on screen.
	cancel := func(b buttons) {
		if b.has("Cancel") && hasMic(b) {
			ui.press("Cancel")
		}
	}
	if b, err = ui.waitFor(8*time.Second, inLobby); err != nil {
		cancel(b)
		return fmt.Errorf("pressed %q but Signal's call lobby didn't appear as expected; backed out", header)
	}

	// Each switch's title names what pressing it would do, so it also reports state.
	wantMic, flipMic := toggle(o.mic, "Mute mic", "Unmute mic")
	wantCam, flipCam := toggle(o.camera, "Turn off camera", "Turn on camera")
	switches := [][2]string{{wantMic, flipMic}, {wantCam, flipCam}}
	if start {
		// ponytail: a group too large to ring has no ringing switch, so start
		// refuses it rather than assume; teach it that lobby when it comes up.
		wantRing, flipRing := toggle(o.ring, "Turn off ringing", "Turn on ringing")
		switches = append(switches, [2]string{wantRing, flipRing})
	}
	for _, s := range switches {
		if b.has(s[0]) {
			continue
		}
		if _, err := ui.press(s[1]); err != nil {
			return err
		}
	}
	ready := func(b buttons) bool {
		for _, s := range switches {
			if !b.has(s[0]) {
				return false
			}
		}
		return inLobby(b)
	}
	if b, err = ui.waitFor(5*time.Second, ready); err != nil {
		cancel(b)
		return errors.New("couldn't confirm the mic/camera/ringing settings in Signal's lobby; cancelled without entering the call")
	}
	if _, err := ui.press(confirm); err != nil {
		return err
	}
	if err := waitDesktop(desktopJoined, 15*time.Second); err != nil {
		return fmt.Errorf("pressed %q in the lobby, but Signal hasn't confirmed it is on the call in %q: %w", confirm, c.Name, err)
	}
	return nil
}

// leaveCall takes Signal Desktop out of the call (or pre-join lobby) it is in.
func leaveCall() error {
	l, err := readCallLog()
	if err != nil {
		return err
	}
	button := map[string]string{desktopJoined: "Leave", desktopLobby: "Cancel"}[l.desktopState()]
	if button == "" {
		return errors.New("Signal Desktop isn't on a call — nothing to leave")
	}
	ui, err := openSignalUI()
	if err != nil {
		return err
	}
	// ponytail: finds the control only while the call screen is showing, not once
	// it is minimized to the floating mini-window; restore it first if so.
	if ok, err := ui.press(button); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("Signal Desktop is on a call but its %s button isn't on screen — bring the call window back and retry", button)
	}
	if err := waitDesktop(desktopNone, 10*time.Second); err != nil {
		return fmt.Errorf("pressed %s, but Signal hasn't confirmed it left: %w", button, err)
	}
	return nil
}

// waitDesktop polls Signal's log until the desktop reaches the wanted state.
func waitDesktop(want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		l, err := readCallLog()
		if err != nil {
			return err
		}
		if l.desktopState() == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still %q after %s", l.desktopState(), timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
