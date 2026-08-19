// Package scope binds a run to a declared set of readable conversations.
//
// The point is that "what can this run read" is something you *read*, not
// something you compute: exactly one file applies, chosen by $LURK_CONFIG or
// the personal default, and per-invocation flags can only narrow it further.
// With no file, lurk behaves as it always has and reads everything.
package scope

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// DefaultPath is the personal default, used when $LURK_CONFIG is unset.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "lurk", "scope")
}

type wsScope struct {
	Token string
	All   bool            // bare workspace line: every channel in it
	Chans map[string]bool // normalized channel names/ids
}

type sigEntry struct {
	Token string
	key   string
}

// Scope is the parsed file. A nil *Scope means unrestricted, so every method
// below is nil-safe and callers don't branch.
type Scope struct {
	Path   string
	slack  []*wsScope
	signal []sigEntry
}

var (
	cur      *Scope
	excluded atomic.Int64
)

// Load resolves the one config that applies to this run: $LURK_CONFIG if set,
// else ~/.config/lurk/scope, else nothing. An unreadable $LURK_CONFIG is always
// fatal — a typo'd path is a mistake, never an intent to read everything.
func Load() error {
	path := os.Getenv("LURK_CONFIG")
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	f, err := os.Open(path)
	if err != nil {
		if explicit {
			return fmt.Errorf("LURK_CONFIG=%s: %w", path, err)
		}
		if required() {
			return fmt.Errorf("LURK_REQUIRE_SCOPE is set but no scope config resolved (looked at %s); "+
				"set LURK_CONFIG or create that file", path)
		}
		return nil
	}
	defer f.Close()
	s, err := parse(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	s.Path = path
	cur = s
	return nil
}

func required() bool { return os.Getenv("LURK_REQUIRE_SCOPE") == "1" }

// Current returns the scope in force, or nil when unrestricted.
func Current() *Scope { return cur }

func Active() bool { return cur != nil }

// parse reads the line format: a source word, then the rest of the line as the
// entry. Rest-of-line means Signal conversation names with spaces need no
// quoting, which is most of why this isn't TOML.
func parse(r io.Reader) (*Scope, error) {
	s := &Scope{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i == 0 {
			continue // whole-line comment; '#' mid-line is a channel name
		}
		if line == "" {
			continue
		}
		src, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return nil, fmt.Errorf("line %d: %q names no conversation", n, line)
		}
		switch strings.ToLower(src) {
		case "slack":
			ws, ch, hasCh := strings.Cut(rest, "/")
			ws = strings.TrimSpace(ws)
			e := s.ws(ws)
			if !hasCh {
				e.All = true
				continue
			}
			ch = normChan(ch)
			if ch == "" {
				return nil, fmt.Errorf("line %d: %q names no channel after /", n, line)
			}
			e.Chans[ch] = true
		case "signal":
			s.signal = append(s.signal, sigEntry{Token: rest, key: strings.ToLower(rest)})
		default:
			return nil, fmt.Errorf("line %d: unknown source %q (want \"slack\" or \"signal\")", n, src)
		}
	}
	return s, sc.Err()
}

func (s *Scope) ws(token string) *wsScope {
	for _, e := range s.slack {
		if strings.EqualFold(e.Token, token) {
			return e
		}
	}
	e := &wsScope{Token: token, Chans: map[string]bool{}}
	s.slack = append(s.slack, e)
	return e
}

func normChan(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "#"))
}

// SlackWorkspace reports whether a workspace may be read, and which channels.
// A nil channel set means every channel in it. Callers hold the result rather
// than re-matching per channel.
func (s *Scope) SlackWorkspace(team, teamID, url string) (chans map[string]bool, ok bool) {
	if s == nil {
		return nil, true
	}
	for _, e := range s.slack {
		if !matchWS(e.Token, team, teamID, url) {
			continue
		}
		if e.All {
			return nil, true
		}
		return e.Chans, true
	}
	return nil, false
}

// matchWS accepts the workspace name, its team ID, or its slack.com subdomain,
// all case-insensitively — the three things `lurk slack workspaces` shows.
func matchWS(token, team, teamID, url string) bool {
	if strings.EqualFold(token, team) || strings.EqualFold(token, teamID) {
		return true
	}
	host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	host, _, _ = strings.Cut(host, "/")
	sub, _, _ := strings.Cut(host, ".")
	return sub != "" && strings.EqualFold(token, sub)
}

// SlackChannel reports whether one channel is in scope, given the workspace's
// channel set from SlackWorkspace. Either the id or the name may match.
func SlackChannel(chans map[string]bool, id, name string) bool {
	if chans == nil {
		return true
	}
	return chans[normChan(id)] || (name != "" && chans[normChan(name)])
}

// Signal reports whether a Signal conversation is in scope. Its display name,
// its id, or its phone number may match.
func (s *Scope) Signal(id, name, e164 string) bool {
	if s == nil {
		return true
	}
	for _, e := range s.signal {
		if strings.EqualFold(e.Token, name) || strings.EqualFold(e.Token, id) ||
			(e164 != "" && strings.EqualFold(e.Token, e164)) {
			return true
		}
	}
	return false
}

// SignalTokens is the raw list, for describing the scope and for narrowing a
// query before it runs.
func (s *Scope) SignalTokens() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, e := range s.signal {
		out = append(out, e.Token)
	}
	return out
}

// Exclude records conversations dropped by scope, so "skipped" stays
// distinguishable from "nothing waiting". Atomic: Slack digests workspaces
// concurrently.
func Exclude(n int) {
	if n > 0 {
		excluded.Add(int64(n))
	}
}

// Report prints the exclusion count, once, at the end of a run.
func Report(w io.Writer) {
	if n := excluded.Load(); n > 0 {
		fmt.Fprintf(w, "%d result%s excluded by scope (%s)\n", n, plural(n), cur.Path)
	}
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Refuse is the answer for read paths that can't be bound — `slack raw` takes an
// arbitrary channel param, `signal raw` an arbitrary SELECT. Rather than let
// them silently bypass the floor, they're refused while a scope is in force.
func Refuse(what string) error {
	if !Active() {
		return nil
	}
	return fmt.Errorf("%s takes an unbounded target, so it's refused while a scope is in force (%s); "+
		"run it without LURK_CONFIG set if you mean to reach outside", what, cur.Path)
}

// Describe prints which file applies and what it resolves to. Channel IDs are
// unreadable enough that a wrong-but-plausible config would go unnoticed, so
// being able to see this before a run is what makes the scope checkable.
func Describe(w io.Writer) {
	if cur == nil {
		fmt.Fprintf(w, "no scope in force — lurk reads everything you're signed into.\n\n")
		fmt.Fprintf(w, "looked at: $LURK_CONFIG (unset)\n           %s (absent)\n", DefaultPath())
		return
	}
	src := "default"
	if os.Getenv("LURK_CONFIG") != "" {
		src = "$LURK_CONFIG"
	}
	fmt.Fprintf(w, "scope: %s  (via %s)\n", cur.Path, src)
	if required() {
		fmt.Fprintln(w, "LURK_REQUIRE_SCOPE=1 — an unresolvable config would have been fatal")
	}
	fmt.Fprintln(w)
	for _, e := range cur.slack {
		if e.All {
			fmt.Fprintf(w, "slack  %-24s  (every channel)\n", e.Token)
			continue
		}
		var names []string
		for c := range e.Chans {
			names = append(names, "#"+c)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "slack  %-24s  %s\n", e.Token, strings.Join(names, " "))
	}
	for _, e := range cur.signal {
		fmt.Fprintf(w, "signal %s\n", e.Token)
	}
}
