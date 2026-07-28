package slack

// Thread codes: short, session-scoped handles for threads.
//
// Every command that lists threads (search, history, mentions) prints a tiny
// integer next to each one; `lurk slack replies <code>` then reopens that
// thread without the caller having to transcribe a 16-digit permalink id or
// hand-format a decimal thread_ts. Agents — the primary caller — fumble long
// numbers far more often than a one- or two-digit code, so the code is the
// low-error fast path; the full permalink stays printed as a durable fallback.
//
// Codes are scoped to the Claude Code session (CLAUDE_CODE_SESSION_ID), so two
// agents lurking on one machine never share a numbering. Within a session the
// counter only ever climbs: a thread keeps the same code for the session's
// life, and a code always means exactly one thread — nothing to mix up.
//
// The cache is a convenience, never a dependency: any filesystem error degrades
// to "no code shown", never a failed read.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// locator is everything `replies` needs to reopen a thread from a bare code.
type locator struct {
	Workspace string `json:"workspace"`
	Channel   string `json:"channel"`
	ThreadTS  string `json:"thread_ts"`
	Permalink string `json:"permalink,omitempty"`
}

type codeEntry struct {
	Code int `json:"code"`
	locator
}

type codeStore struct {
	Next    int                  `json:"next"`
	Entries map[string]codeEntry `json:"entries"` // key: channel|thread_ts
}

// codeMu guards read-modify-write of the cache file. `lurk summary` gathers
// workspaces in parallel goroutines, so assignment must be safe within a
// process; cross-process races don't arise because each session writes its own
// file and an agent's calls are sequential.
var codeMu sync.Mutex

// staleAfter is how long an unused session file lingers before pruning. A
// session that runs longer than this keeps rewriting its own file, so its
// mtime stays fresh — only truly dead sessions get swept.
const staleAfter = 7 * 24 * time.Hour

func codesDir() string { return filepath.Join(os.TempDir(), "lurk") }

// sessionID names the current caller's cache file. It prefers the Claude Code
// session UUID; LURK_SESSION lets a non-agent caller opt into the same scheme;
// "default" is the shared fallback when neither is set.
func sessionID() string {
	for _, env := range []string{"CLAUDE_CODE_SESSION_ID", "LURK_SESSION"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return sanitize(v)
		}
	}
	return "default"
}

// sanitize keeps a session id safe as a filename component.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
}

func codeFile() string { return filepath.Join(codesDir(), "codes-"+sessionID()+".json") }

func loadStore(path string) codeStore {
	s := codeStore{Next: 1, Entries: map[string]codeEntry{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	if s.Entries == nil {
		s.Entries = map[string]codeEntry{}
	}
	if s.Next < 1 {
		s.Next = 1
	}
	return s
}

func saveStore(path string, s codeStore) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	// Atomic-ish write so a concurrent reader never sees a half-file.
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// assignCode returns a stable session code for a thread, minting one on first
// sight. It returns 0 (meaning "no code") if caching is unavailable — callers
// treat 0 as "show nothing" and carry on.
func assignCode(loc locator) int {
	if loc.Channel == "" || loc.ThreadTS == "" {
		return 0
	}
	codeMu.Lock()
	defer codeMu.Unlock()

	if err := os.MkdirAll(codesDir(), 0o700); err != nil {
		return 0
	}
	pruneStale()

	path := codeFile()
	s := loadStore(path)
	key := loc.Channel + "|" + loc.ThreadTS
	if e, ok := s.Entries[key]; ok {
		// Refresh the permalink if we learned one since (some paths lack it).
		if loc.Permalink != "" && e.Permalink == "" {
			e.locator = loc
			s.Entries[key] = e
			saveStore(path, s)
		}
		return e.Code
	}
	code := s.Next
	s.Next++
	s.Entries[key] = codeEntry{Code: code, locator: loc}
	saveStore(path, s)
	return code
}

// resolveCode looks up a locator by its session code.
func resolveCode(code int) (locator, bool) {
	codeMu.Lock()
	defer codeMu.Unlock()
	s := loadStore(codeFile())
	for _, e := range s.Entries {
		if e.Code == code {
			return e.locator, true
		}
	}
	return locator{}, false
}

// pruneStale sweeps dead sessions' files. Best-effort; caller holds codeMu.
func pruneStale() {
	entries, err := os.ReadDir(codesDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleAfter)
	for _, de := range entries {
		if !strings.HasPrefix(de.Name(), "codes-") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(codesDir(), de.Name()))
		}
	}
}
