// Package slack gives read-only access to the Slack workspaces you're signed
// into on this Mac.
//
// It extracts your own session credentials from the Slack desktop app — the
// per-team `xoxc-` token from the Local Storage leveldb, plus the shared d/d-s
// cookies from the encrypted Cookies DB (decrypted with the "Slack Safe Storage"
// Keychain key) — and uses them to call Slack's Web API.
//
// READ-ONLY BY DESIGN: only methods in readMethods are ever callable; there is
// no code path that posts, edits, joins, or deletes anything. Secrets live in
// memory only for the life of the process — never written to disk or logged.
//
// Deps: Go stdlib + goleveldb (to read the snappy-compressed token store) +
// `security` + `sqlite3` (both on macOS).
package slack

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/akostibas/lurk-skill/internal/digest"
	"github.com/akostibas/lurk-skill/internal/macos"
	"github.com/akostibas/lurk-skill/internal/scope"
)

// slackSupport is a function rather than a package var so it tracks HOME at
// call time, matching signalDir() and letting tests point it at a fixture.
func slackSupport() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Slack")
}

var tokenRe = regexp.MustCompile(`xoxc-\d+-\d+-\d+-[0-9a-f]+`)

// Matches <@U123> and the labelled <@U123|name> form Slack search returns.
var mentionRe = regexp.MustCompile(`<@([A-Z][A-Z0-9]+)(?:\|([^>]*))?>`)

// The ONLY Slack API methods this tool will ever call. All are read-only.
// Adding a write method here would defeat the tool's guarantee — don't.
var readMethods = map[string]bool{
	"auth.test":             true,
	"team.info":             true,
	"conversations.list":    true,
	"conversations.info":    true,
	"conversations.history": true,
	"conversations.replies": true,
	"conversations.members": true,
	"users.list":            true,
	"users.info":            true,
	"users.conversations":   true,
	"files.info":            true,
	"search.messages":       true,
	"search.all":            true,
	"reactions.get":         true,
	"pins.list":             true,
	"bookmarks.list":        true,
	"emoji.list":            true,
	// Internal endpoints the desktop client uses — read-only, power the catch-up
	// summary (unread/mention counts and the Threads view).
	"client.counts":                true,
	"subscriptions.thread.getView": true,
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Credential extraction (local, read-only)
// ---------------------------------------------------------------------------

// extractTokens returns every xoxc- token across all workspaces the user is
// signed into. The tokens live in the Local Storage leveldb, which snappy-
// compresses most entries — so a plain byte-scan only finds recently-written
// (uncompressed) ones. We open the leveldb properly (via a copy, to sidestep
// the lock the running Slack app holds) and decode each value. If that fails,
// we fall back to a raw scan (better than nothing).
func extractTokens() ([]string, error) {
	seen := map[string]bool{}
	var toks []string
	add := func(b []byte) {
		for _, m := range tokenRe.FindAll(b, -1) {
			if s := string(m); !seen[s] {
				seen[s] = true
				toks = append(toks, s)
			}
		}
	}

	db, closeDB, err := openLevelDBCopy()
	if err == nil {
		defer closeDB()
		it := db.NewIterator(nil, nil)
		for it.Next() {
			add(decodeLSValue(it.Value()))
		}
		it.Release()
	}
	if len(toks) == 0 {
		extractTokensRaw(add) // fallback: uncompressed entries only
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("no xoxc tokens in Slack desktop storage — is the app installed and signed in?")
	}
	return toks, nil
}

// openLevelDBCopy copies the Local Storage leveldb to a temp dir (dropping the
// LOCK file) and opens it read-only, so we don't fight the running app's lock.
func openLevelDBCopy() (*leveldb.DB, func(), error) {
	src := filepath.Join(slackSupport(), "Local Storage", "leveldb")
	tmp, err := os.MkdirTemp("", "slackread-*")
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "LOCK" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(tmp, e.Name()), b, 0o600)
	}
	db, err := leveldb.OpenFile(tmp, &opt.Options{ReadOnly: true})
	if err != nil {
		os.RemoveAll(tmp)
		return nil, nil, err
	}
	return db, func() { db.Close(); os.RemoveAll(tmp) }, nil
}

// decodeLSValue decodes a Chromium Local Storage value. Chromium prefixes string
// values with 0x00 (UTF-16LE) or 0x01 (Latin-1/UTF-8). We only care about ASCII
// token bytes, so UTF-16 is flattened by dropping the zero high bytes.
func decodeLSValue(v []byte) []byte {
	if len(v) == 0 {
		return v
	}
	if v[0] == 0 { // UTF-16LE
		out := make([]byte, 0, len(v)/2)
		for i := 1; i+1 < len(v); i += 2 {
			if v[i+1] == 0 {
				out = append(out, v[i])
			} else {
				out = append(out, '?')
			}
		}
		return out
	}
	return v[1:]
}

// extractTokensRaw scans the leveldb files as raw bytes — finds only tokens in
// uncompressed blocks, used only if the proper leveldb read fails.
func extractTokensRaw(add func([]byte)) {
	glob := filepath.Join(slackSupport(), "Local Storage", "leveldb", "*")
	files, _ := filepath.Glob(glob)
	for _, f := range files {
		if ext := filepath.Ext(f); ext != ".ldb" && ext != ".log" {
			continue
		}
		if b, err := os.ReadFile(f); err == nil {
			add(b)
		}
	}
}

// pbkdf2SHA1 derives a key the same way Chromium does for cookie encryption.
func pbkdf2SHA1(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha1.Size
	blocks := (keyLen + hashLen - 1) / hashLen
	var dk []byte
	for block := 1; block <= blocks; block++ {
		prf := hmac.New(sha1.New, password)
		prf.Write(salt)
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		prf.Write(idx[:])
		u := prf.Sum(nil)
		t := append([]byte(nil), u...)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// storageKey reads the "Slack Safe Storage" Keychain password for the given
// account ("" = first match) and derives the AES key. The Slack build (App
// Store vs direct download) determines which account name holds the real key,
// and stale entries from a previous build can linger — so callers try accounts
// in order and stop at the first that actually decrypts a cookie.
func storageKey(acct string) []byte {
	args := []string{"find-generic-password", "-s", "Slack Safe Storage"}
	if acct != "" {
		args = append(args, "-a", acct)
	}
	args = append(args, "-w")
	out, err := exec.Command("security", args...).Output()
	if err != nil {
		return nil
	}
	pw := bytes.TrimSpace(out)
	if len(pw) == 0 {
		return nil
	}
	return pbkdf2SHA1(pw, []byte("saltysalt"), 1003, 16)
}

// decryptV10 decrypts a Chromium v10 AES-128-CBC cookie value to raw bytes.
func decryptV10(enc, key []byte) []byte {
	if !bytes.HasPrefix(enc, []byte("v10")) {
		return nil
	}
	ct := enc[3:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil
	}
	iv := bytes.Repeat([]byte{0x20}, aes.BlockSize)
	dec := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(dec, ct)
	return dec
}

// cleanValue strips a possible 32-byte SHA256(domain) prefix (Chromium 130+) and
// PKCS7 padding, returning the leading printable-ASCII run — the cookie value.
func cleanValue(dec []byte) string {
	if len(dec) > 32 && (dec[0] < 0x20 || dec[0] >= 0x7f) && dec[32] >= 0x20 && dec[32] < 0x7f {
		dec = dec[32:]
	}
	var b strings.Builder
	for _, c := range dec {
		if c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		} else {
			break
		}
	}
	return b.String()
}

func cookieHeader() (string, error) {
	db := filepath.Join(slackSupport(), "Cookies")
	q := "SELECT name || '|' || hex(encrypted_value) FROM cookies WHERE host_key LIKE '%slack.com' AND name IN ('d','d-s');"
	out, err := exec.Command("sqlite3", "-readonly", db, q).Output()
	if err != nil {
		return "", fmt.Errorf("reading Slack's cookie database at %s — is the desktop app installed and signed in? (%w)", db, err)
	}
	enc := map[string][]byte{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, hexval, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		if raw, err := hex.DecodeString(hexval); err == nil {
			enc[name] = raw
		}
	}
	if enc["d"] == nil {
		return "", fmt.Errorf("no Slack 'd' session cookie found")
	}
	// Try accounts in order; the correct key decrypts `d` to an xoxd- token.
	// Stop at the first hit so a normal run triggers a single Keychain read.
	var key []byte
	for _, acct := range []string{"Slack Key", "Slack App Store Key", ""} {
		k := storageKey(acct)
		if k != nil && strings.HasPrefix(cleanValue(decryptV10(enc["d"], k)), "xoxd-") {
			key = k
			break
		}
	}
	if key == nil {
		// Distinguish "the key was never stored" (Slack has never run here)
		// from "the key is there but doesn't fit" (a stale entry from an
		// older Slack build) — the two need different advice.
		if _, err := macos.Secret("Slack Safe Storage"); err != nil {
			return "", fmt.Errorf("no usable Slack encryption key: %w", err)
		}
		return "", errors.New("the Slack Keychain key doesn't decrypt this session cookie — sign out and back in to the desktop app")
	}
	parts := []string{"d=" + cleanValue(decryptV10(enc["d"], key))}
	if enc["d-s"] != nil {
		if v := cleanValue(decryptV10(enc["d-s"], key)); v != "" {
			parts = append(parts, "d-s="+v)
		}
	}
	return strings.Join(parts, "; "), nil
}

// ---------------------------------------------------------------------------
// API client (read-only)
// ---------------------------------------------------------------------------

type client struct {
	token  string
	cookie string

	// scope, set once by buildRegistry. chans is the declared channel set for
	// this workspace (nil = every channel); allowed is that set resolved to
	// channel IDs, built lazily on the first content call.
	chans   map[string]bool
	allowed map[string]bool
	once    sync.Once
}

// contentMethods return message bodies. They're the ones scope binds: everything
// else in readMethods returns metadata (names, counts, membership) that a caller
// already sees in `slack channels`, and gating them would make resolving a
// channel's name impossible without recursing through this check.
var contentMethods = map[string]bool{
	"conversations.history": true,
	"conversations.replies": true,
}

func (c *client) call(method string, params map[string]string) (map[string]any, error) {
	if !readMethods[method] {
		return nil, fmt.Errorf("refusing to call non-read method %q; this tool is read-only", method)
	}
	if c.chans != nil && contentMethods[method] {
		if ch := params["channel"]; ch != "" && !c.channelAllowed(ch) {
			return nil, fmt.Errorf("channel %s is outside the declared scope (see: lurk scope)", ch)
		}
	}
	form := url.Values{}
	for k, v := range params {
		if v != "" {
			form.Set(k, v)
		}
	}
	form.Set("token", c.token)

	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequest("POST", "https://slack.com/api/"+method, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Cookie", c.cookie)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) slack-read/1.0")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 && attempt < 3 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			if wait <= 0 {
				wait = 2
			}
			time.Sleep(time.Duration(wait) * time.Second)
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("%s: bad response", method)
		}
		if ok, _ := out["ok"].(bool); !ok {
			return nil, fmt.Errorf("%s: %v", method, out["error"])
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: rate-limited after retries", method)
}

// channelAllowed answers the gate above for one channel ID. The config names
// channels the way a person does (#eng), so the first content call resolves the
// whole workspace's channel list once and keeps the ID set.
func (c *client) channelAllowed(id string) bool {
	if scope.SlackChannel(c.chans, id, "") {
		return true
	}
	c.once.Do(func() {
		c.allowed = map[string]bool{}
		cursor := ""
		for {
			r, err := c.call("conversations.list", map[string]string{
				"types": "public_channel,private_channel,mpim,im", "limit": "1000", "cursor": cursor,
			})
			if err != nil {
				return
			}
			for _, chv := range asList(r["channels"]) {
				ch, _ := chv.(map[string]any)
				if cid := str(ch["id"]); scope.SlackChannel(c.chans, cid, str(ch["name"])) {
					c.allowed[cid] = true
				}
			}
			if cursor = nextCursor(r); cursor == "" {
				return
			}
		}
	})
	return c.allowed[id]
}

// download fetches a Slack file's raw bytes over HTTPS using the session's
// bearer token + cookie. Slack serves file contents (url_private) not from the
// Web API but from files.slack.com, gated on `Authorization: Bearer <token>`.
// This is a plain authenticated GET — the same class of access as viewing the
// file in the app, a read — not an API write, so it sits outside the readMethods
// gate (which guards api.slack.com method calls). It is restricted to Slack's
// own file hosts so a caller can't turn it into an arbitrary URL fetcher.
func (c *client) download(fileURL string) (body []byte, contentType string, err error) {
	u, err := url.Parse(fileURL)
	if err != nil {
		return nil, "", fmt.Errorf("bad file url %q: %w", fileURL, err)
	}
	if u.Scheme != "https" || !isSlackFileHost(u.Host) {
		return nil, "", fmt.Errorf("refusing to fetch %q; downloads are limited to Slack file hosts", fileURL)
	}
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) slack-read/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	contentType = resp.Header.Get("Content-Type")
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, contentType, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, contentType, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	// When the token/cookie is rejected, Slack answers 200 with an HTML sign-in
	// page rather than an error — detect that so we never hand back a login page
	// as if it were the file bytes.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/html") {
		return nil, contentType, errors.New("download returned an HTML page, not file bytes — the session/token was rejected (try signing out and back in to the Slack desktop app)")
	}
	return b, contentType, nil
}

// isSlackFileHost limits downloads to Slack's own file-serving hosts.
func isSlackFileHost(host string) bool {
	host = strings.ToLower(host)
	return host == "files.slack.com" || host == "slack-files.com" ||
		strings.HasSuffix(host, ".slack.com") || strings.HasSuffix(host, ".slack-files.com")
}

// ---------------------------------------------------------------------------
// Workspace registry
// ---------------------------------------------------------------------------

type workspace struct {
	Team, TeamID, URL, User, UserID string
	c                               *client
}

// allows reports whether one of this workspace's channels is inside the
// declared scope. Either its id or its name may be what the config names.
func (w workspace) allows(id, name string) bool {
	return scope.SlackChannel(w.c.chans, id, name)
}

// checkInstalled reports a missing Slack desktop app in those terms, rather
// than letting the absence surface several layers down as a sqlite3 or
// Keychain failure the user can't act on.
func checkInstalled() error {
	if _, err := os.Stat(slackSupport()); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the Slack desktop app isn't installed (no %s)", slackSupport())
	}
	if _, err := os.Stat(filepath.Join(slackSupport(), "Cookies")); errors.Is(err, os.ErrNotExist) {
		return errors.New("Slack is installed but has no saved session — open the app and sign in")
	}
	return nil
}

func buildRegistry() ([]workspace, error) {
	if err := checkInstalled(); err != nil {
		return nil, err
	}
	cookie, err := cookieHeader()
	if err != nil {
		return nil, err
	}
	toks, err := extractTokens()
	if err != nil {
		return nil, err
	}
	var reg []workspace
	dropped := 0
	for _, t := range toks {
		c := &client{token: t, cookie: cookie}
		info, err := c.call("auth.test", nil)
		if err != nil {
			continue // stale/invalid token
		}
		w := workspace{
			Team:   str(info["team"]),
			TeamID: str(info["team_id"]),
			URL:    str(info["url"]),
			User:   str(info["user"]),
			UserID: str(info["user_id"]),
			c:      c,
		}
		// Workspaces are gated here, at the one place the registry is built, so
		// no command can reach a workspace the config doesn't name.
		chans, ok := scope.Current().SlackWorkspace(w.Team, w.TeamID, w.URL)
		if !ok {
			dropped++
			continue
		}
		c.chans = chans
		reg = append(reg, w)
	}
	scope.Exclude(dropped)
	if len(reg) == 0 {
		if dropped > 0 {
			return nil, fmt.Errorf("no signed-in workspace is in scope (%d excluded; see: lurk scope)", dropped)
		}
		return nil, fmt.Errorf("found tokens but none authenticated — open the Slack desktop app, sign in, and retry")
	}
	sort.Slice(reg, func(i, j int) bool { return reg[i].Team < reg[j].Team })
	return reg, nil
}

func pick(reg []workspace, name string) (workspace, error) {
	n := strings.ToLower(name)
	for _, w := range reg {
		if strings.Contains(strings.ToLower(w.Team), n) || strings.Contains(strings.ToLower(w.URL), n) {
			return w, nil
		}
	}
	var names []string
	for _, w := range reg {
		names = append(names, w.Team)
	}
	return workspace{}, fmt.Errorf("no workspace matching %q; available: %s", name, strings.Join(names, ", "))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func str(v any) string {
	s, _ := v.(string)
	return s
}

// tsLayout is the timestamp format in every text-mode line. It is a de-facto
// output contract: `slack search|history|replies` print it as a leading
// "[YYYY-MM-DD HH:MM]" and downstream callers parse it to window results.
// Changing it fails *silently* — a caller's filter simply matches nothing and
// the run looks like a quiet day rather than an error. Keep it in step with
// signal's tsLayout, and see the tests pinning both.
const tsLayout = "2006-01-02 15:04"

func fmtTS(ts string) string {
	sec, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return ts
	}
	return time.Unix(n, 0).Format(tsLayout)
}

func userMap(c *client) map[string]string {
	names := map[string]string{}
	cursor := ""
	for {
		r, err := c.call("users.list", map[string]string{"limit": "200", "cursor": cursor})
		if err != nil {
			break
		}
		for _, mv := range asList(r["members"]) {
			m, _ := mv.(map[string]any)
			id := str(m["id"])
			prof, _ := m["profile"].(map[string]any)
			name := str(prof["display_name"])
			if name == "" {
				name = str(prof["real_name"])
			}
			if name == "" {
				name = str(m["name"])
			}
			if name == "" {
				name = id
			}
			names[id] = name
		}
		cursor = nextCursor(r)
		if cursor == "" {
			break
		}
	}
	return names
}

func renderText(text string, names map[string]string) string {
	return mentionRe.ReplaceAllStringFunc(text, func(s string) string {
		m := mentionRe.FindStringSubmatch(s)
		if n, ok := names[m[1]]; ok {
			return "@" + n
		}
		if m[2] != "" { // inline label
			return "@" + m[2]
		}
		return s
	})
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func nextCursor(r map[string]any) string {
	meta, _ := r["response_metadata"].(map[string]any)
	return str(meta["cursor"])
}

var chanIDRe = regexp.MustCompile(`^[CDG][A-Z0-9]+$`)

func resolveChannel(c *client, ident string) (id, name string, err error) {
	if chanIDRe.MatchString(ident) {
		info, err := c.call("conversations.info", map[string]string{"channel": ident})
		if err != nil {
			return ident, ident, nil
		}
		ch, _ := info["channel"].(map[string]any)
		nm := str(ch["name"])
		if nm == "" {
			nm = ident
		}
		return ident, nm, nil
	}
	want := strings.ToLower(strings.TrimPrefix(ident, "#"))
	cursor := ""
	for {
		r, err := c.call("conversations.list", map[string]string{
			"types": "public_channel,private_channel,mpim,im", "limit": "1000", "cursor": cursor,
		})
		if err != nil {
			return "", "", err
		}
		for _, chv := range asList(r["channels"]) {
			ch, _ := chv.(map[string]any)
			if strings.ToLower(str(ch["name"])) == want {
				return str(ch["id"]), str(ch["name"]), nil
			}
		}
		cursor = nextCursor(r)
		if cursor == "" {
			break
		}
	}
	return "", "", fmt.Errorf("no channel named %q in this workspace", ident)
}

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func cmdWorkspaces(reg []workspace, asJSON bool) {
	if asJSON {
		var out []map[string]string
		for _, w := range reg {
			out = append(out, map[string]string{"team": w.Team, "team_id": w.TeamID, "url": w.URL, "user": w.User})
		}
		printJSON(out)
		return
	}
	for _, w := range reg {
		fmt.Printf("%-28s %-40s (you: %s)\n", w.Team, w.URL, w.User)
	}
}

func cmdChannels(w workspace, types, filter string, asJSON bool) {
	var chans []any
	cursor := ""
	for {
		r, err := w.c.call("conversations.list", map[string]string{
			"types": types, "limit": "1000", "cursor": cursor, "exclude_archived": "true",
		})
		if err != nil {
			fail(err)
		}
		chans = append(chans, asList(r["channels"])...)
		cursor = nextCursor(r)
		if cursor == "" {
			break
		}
	}
	type row struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Private  bool   `json:"is_private"`
		IsMember bool   `json:"is_member"`
	}
	var rows []row
	f := strings.ToLower(filter)
	for _, cv := range chans {
		ch, _ := cv.(map[string]any)
		name := str(ch["name"])
		if name == "" {
			name = str(ch["user"])
		}
		if name == "" {
			name = str(ch["id"])
		}
		if f != "" && !strings.Contains(strings.ToLower(name), f) {
			continue
		}
		if !w.allows(str(ch["id"]), str(ch["name"])) {
			scope.Exclude(1)
			continue
		}
		priv, _ := ch["is_private"].(bool)
		mem, _ := ch["is_member"].(bool)
		rows = append(rows, row{str(ch["id"]), name, priv, mem})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if asJSON {
		printJSON(rows)
		return
	}
	for _, r := range rows {
		mark := "#"
		if r.Private {
			mark = "🔒"
		}
		member := ""
		if !r.IsMember {
			member = "  (not a member)"
		}
		fmt.Printf("%-12s %s%s%s\n", r.ID, mark, r.Name, member)
	}
}

func cmdHistory(w workspace, channel string, limit int, oldest, cursor string, asJSON bool) {
	cid, cname, err := resolveChannel(w.c, channel)
	if err != nil {
		fail(err)
	}
	r, err := w.c.call("conversations.history", map[string]string{
		"channel": cid, "limit": strconv.Itoa(limit), "oldest": oldest, "cursor": cursor,
	})
	if err != nil {
		fail(err)
	}
	msgs := asList(r["messages"])
	// API returns newest-first; show oldest-first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	// A message with replies is a thread root — give it a session code so it can
	// be reopened with `lurk slack replies <code>`. Its own ts is the root ts.
	for _, mv := range msgs {
		m, _ := mv.(map[string]any)
		if rc, ok := m["reply_count"].(float64); ok && rc > 0 {
			if code := assignCode(locator{Workspace: w.Team, Channel: cid, ThreadTS: str(m["ts"])}); code > 0 {
				m["lurk_code"] = code
			}
		}
	}
	if asJSON {
		printJSON(map[string]any{"channel": cname, "channel_id": cid, "messages": msgs, "next_cursor": nextCursor(r)})
		return
	}
	names := userMap(w.c)
	fmt.Printf("# %s (%s) — %d messages\n\n", cname, cid, len(msgs))
	for _, mv := range msgs {
		m, _ := mv.(map[string]any)
		who := names[str(m["user"])]
		if who == "" {
			who = firstNonEmpty(str(m["username"]), str(m["bot_id"]), "?")
		}
		fmt.Printf("[%s] %s: %s\n", fmtTS(str(m["ts"])), who, renderText(str(m["text"]), names))
		if rc, ok := m["reply_count"].(float64); ok && rc > 0 {
			if code, ok := m["lurk_code"].(int); ok && code > 0 {
				fmt.Printf("    ↳ %d replies — reopen with: lurk slack replies %d\n", int(rc), code)
			} else {
				fmt.Printf("    ↳ %d replies (thread_ts %s)\n", int(rc), str(m["ts"]))
			}
		}
	}
	if nc := nextCursor(r); nc != "" {
		fmt.Printf("\n(more: --cursor %s)\n", nc)
	}
}

func cmdReplies(w workspace, channel, threadTS string, limit int, asJSON bool) {
	cid, cname, err := resolveChannel(w.c, channel)
	if err != nil {
		fail(err)
	}
	r, err := w.c.call("conversations.replies", map[string]string{
		"channel": cid, "ts": threadTS, "limit": strconv.Itoa(limit),
	})
	if err != nil {
		fail(err)
	}
	msgs := asList(r["messages"])
	if asJSON {
		printJSON(msgs)
		return
	}
	names := userMap(w.c)
	fmt.Printf("# thread in %s (%s)\n\n", cname, cid)
	for _, mv := range msgs {
		m, _ := mv.(map[string]any)
		who := names[str(m["user"])]
		if who == "" {
			who = firstNonEmpty(str(m["username"]), "?")
		}
		fmt.Printf("[%s] %s: %s\n", fmtTS(str(m["ts"])), who, renderText(str(m["text"]), names))
	}
}

// cmdFile retrieves a Slack attachment and writes its bytes to disk — the one
// piece of a message the text-only commands can't surface. It accepts either a
// file ID (resolved to its private URL via files.info) or a url_private /
// url_private_download URL already in hand from `--json` message output.
// Read-only: it only GETs the file, the same access the user has in the app.
func cmdFile(w workspace, ref, out string, asJSON bool) {
	fileURL := ref
	var name, mimetype string
	refIsURL := strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://")
	if refIsURL && w.c.chans != nil {
		// A url_private carries no channel, so there's nothing to check it
		// against. Under a scope, go via the file ID instead.
		fail(fmt.Errorf("a scope is in force, so `slack file` needs a file ID (not a URL) to check it against (see: lurk scope)"))
	}
	// A bare (non-URL) ref is a file ID — resolve it to a download URL.
	if !refIsURL {
		info, err := w.c.call("files.info", map[string]string{"file": ref})
		if err != nil {
			fail(err)
		}
		f, _ := info["file"].(map[string]any)
		// A file ID names no channel, so bind it to the channels the file was
		// shared in — otherwise `slack file` reaches past the scope by ID.
		if !fileInScope(w, f) {
			fail(fmt.Errorf("file %q isn't shared in any channel in scope (see: lurk scope)", ref))
		}
		fileURL = firstNonEmpty(str(f["url_private_download"]), str(f["url_private"]))
		name = str(f["name"])
		mimetype = str(f["mimetype"])
		if fileURL == "" {
			fail(fmt.Errorf("file %q has no downloadable URL (external, deleted, or no access)", ref))
		}
	}

	body, ct, err := w.c.download(fileURL)
	if err != nil {
		fail(err)
	}
	if mimetype == "" {
		mimetype = ct
	}

	// Choose an output path: honour --out; otherwise land in the temp dir under
	// the file's own name (or the URL basename) so callers get a stable path.
	if out == "" {
		base := name
		if base == "" {
			if u, perr := url.Parse(fileURL); perr == nil {
				base = path.Base(u.Path)
			}
		}
		if base == "" || base == "." || base == "/" {
			base = "lurk-slack-file"
		}
		out = filepath.Join(os.TempDir(), base)
	}
	if out == "-" {
		os.Stdout.Write(body)
		return
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		fail(err)
	}
	abs, _ := filepath.Abs(out)
	if asJSON {
		printJSON(map[string]any{"path": abs, "bytes": len(body), "mimetype": mimetype, "name": name})
		return
	}
	extra := ""
	if mimetype != "" {
		extra = "  (" + mimetype + ")"
	}
	fmt.Printf("saved %d bytes → %s%s\n", len(body), abs, extra)
}

func cmdSearch(w workspace, query string, count int, asJSON bool) {
	r, err := w.c.call("search.messages", map[string]string{
		"query": query, "count": strconv.Itoa(count), "sort": "timestamp",
	})
	if err != nil {
		fail(err)
	}
	mm, _ := r["messages"].(map[string]any)
	matches := scopeMatches(w, asList(mm["matches"]))
	// Attach a session thread code to every match up front, so it lands in both
	// the JSON (`lurk_code`) and the text rendering below.
	for _, mv := range matches {
		m, _ := mv.(map[string]any)
		ch, _ := m["channel"].(map[string]any)
		pl := str(m["permalink"])
		if code := assignCode(locator{
			Workspace: w.Team, Channel: str(ch["id"]),
			ThreadTS: threadRootFromPermalink(pl, str(m["ts"])), Permalink: pl,
		}); code > 0 {
			m["lurk_code"] = code
		}
	}
	if asJSON {
		printJSON(matches)
		return
	}
	// Slack's own total counts channels a scope may have just filtered out.
	total := len(matches)
	if t, ok := mm["total"].(float64); ok && w.c.chans == nil {
		total = int(t)
	}
	fmt.Printf("# %d matches for: %s  (open a thread with: lurk slack replies <code>)\n\n", total, query)
	for _, mv := range matches {
		m, _ := mv.(map[string]any)
		ch, _ := m["channel"].(map[string]any)
		who := firstNonEmpty(str(m["username"]), str(m["user"]), "?")
		fmt.Printf("[%s] #%s %s: %s\n", fmtTS(str(m["ts"])), str(ch["name"]), who, str(m["text"]))
		if tag, pl := codeTag(m["lurk_code"]), str(m["permalink"]); tag != "" || pl != "" {
			fmt.Printf("    %s%s\n", tag, pl)
		}
	}
}

// fileInScope reports whether a files.info result was shared into any channel
// the scope admits.
func fileInScope(w workspace, f map[string]any) bool {
	if w.c.chans == nil {
		return true
	}
	for _, key := range []string{"channels", "groups", "ips"} {
		for _, cv := range asList(f[key]) {
			if w.allows(str(cv), "") {
				return true
			}
		}
	}
	return false
}

// scopeMatches drops search results from channels outside the declared scope.
// Search is the one read path where Slack picks the channels, not the caller,
// so it has to be filtered on the way out rather than gated on the way in.
func scopeMatches(w workspace, matches []any) []any {
	if w.c.chans == nil {
		return matches
	}
	out := matches[:0:0]
	for _, mv := range matches {
		m, _ := mv.(map[string]any)
		ch, _ := m["channel"].(map[string]any)
		if w.allows(str(ch["id"]), str(ch["name"])) {
			out = append(out, mv)
		}
	}
	scope.Exclude(len(matches) - len(out))
	return out
}

// codeTag renders the "[n] " prefix for a thread code pulled from a result map,
// or "" when there's no code. Kept out of the leading "[timestamp]" slot on
// purpose: downstream callers parse that timestamp positionally (see tsLayout),
// so the code rides alongside the permalink instead.
func codeTag(v any) string {
	if code, ok := v.(int); ok && code > 0 {
		return fmt.Sprintf("[%d] ", code)
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// tsTime parses a Slack "seconds.micros" timestamp into a time.Time.
func tsTime(ts string) time.Time {
	sec, _, _ := strings.Cut(ts, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// snippet collapses whitespace and truncates to n runes for one-line display.
func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// resolver lazily maps user/channel IDs to display names (cached), so a summary
// touching a handful of people/channels avoids a full users.list/channels dump.
type resolver struct {
	c     *client
	users map[string]string
	chans map[string]string
}

func newResolver(c *client) *resolver {
	return &resolver{c: c, users: map[string]string{}, chans: map[string]string{}}
}

func (r *resolver) user(id string) string {
	if id == "" {
		return ""
	}
	if v, ok := r.users[id]; ok {
		return v
	}
	nm := id
	if resp, err := r.c.call("users.info", map[string]string{"user": id}); err == nil {
		u, _ := resp["user"].(map[string]any)
		p, _ := u["profile"].(map[string]any)
		nm = firstNonEmpty(str(p["display_name"]), str(p["real_name"]), str(u["name"]), id)
	}
	r.users[id] = nm
	return nm
}

func (r *resolver) channel(id string) string {
	if id == "" {
		return ""
	}
	if v, ok := r.chans[id]; ok {
		return v
	}
	nm := id
	if resp, err := r.c.call("conversations.info", map[string]string{"channel": id}); err == nil {
		ch, _ := resp["channel"].(map[string]any)
		if n := str(ch["name"]); n != "" {
			nm = "#" + n
		} else if u := str(ch["user"]); u != "" {
			nm = "DM @" + r.user(u)
		}
	}
	r.chans[id] = nm
	return nm
}

func (r *resolver) render(text string) string {
	return mentionRe.ReplaceAllStringFunc(text, func(s string) string {
		m := mentionRe.FindStringSubmatch(s)
		if m[2] != "" { // inline label — use it, no lookup needed
			return "@" + m[2]
		}
		if id := m[1]; id[0] == 'U' || id[0] == 'W' {
			return "@" + r.user(id)
		}
		return s
	})
}

// summaryItems builds the "catch up" digest for one workspace, leading with the
// things most likely aimed at the user (mentions, DMs), then threads they're
// active in, then remaining unread channels.
//
// Section-level API failures are skipped rather than fatal: a partial digest is
// more useful than none, and `lurk summary` spans several workspaces where one
// bad session shouldn't sink the rest.
func summaryItems(w workspace, mentionHours, threadHours int) []digest.Item {
	c := w.c
	res := newResolver(c)
	now := time.Now()
	mentionCut := now.Add(-time.Duration(mentionHours) * time.Hour)
	threadCut := now.Add(-time.Duration(threadHours) * time.Hour)

	var items []digest.Item
	add := func(k digest.Kind, when time.Time, who, where, text, note, link string) {
		items = append(items, digest.Item{
			Source: "slack", Scope: w.Team, Kind: k, When: when,
			Who: who, Where: where, Text: text, Note: note, Link: link,
		})
	}

	// 1. Mentions of me (read or unread) in the window, via search.
	q := fmt.Sprintf("<@%s> after:%s", w.UserID, mentionCut.Add(-24*time.Hour).Format("2006-01-02"))
	var mentions []map[string]any
	if r, err := c.call("search.messages", map[string]string{"query": q, "count": "100", "sort": "timestamp"}); err == nil {
		mm, _ := r["messages"].(map[string]any)
		for _, mv := range scopeMatches(w, asList(mm["matches"])) {
			m, _ := mv.(map[string]any)
			if str(m["user"]) == w.UserID || tsTime(str(m["ts"])).Before(mentionCut) {
				continue
			}
			mentions = append(mentions, m)
		}
	}
	sort.Slice(mentions, func(i, j int) bool { return str(mentions[i]["ts"]) > str(mentions[j]["ts"]) })
	for _, m := range mentions {
		ch, _ := m["channel"].(map[string]any)
		who := firstNonEmpty(str(m["username"]), res.user(str(m["user"])), "?")
		add(digest.Mention, tsTime(str(m["ts"])), who, "#"+str(ch["name"]),
			snippet(res.render(str(m["text"])), 160), "", str(m["permalink"]))
	}

	counts, _ := c.call("client.counts", nil)

	// 2. Unread DMs — direct messages waiting on you.
	ims := asList(counts["ims"])
	sort.Slice(ims, func(i, j int) bool {
		a, _ := ims[i].(map[string]any)
		b, _ := ims[j].(map[string]any)
		return str(a["latest"]) > str(b["latest"])
	})
	for _, iv := range ims {
		im, _ := iv.(map[string]any)
		if hu, _ := im["has_unreads"].(bool); !hu {
			continue
		}
		id := str(im["id"])
		if !w.allows(id, "") {
			scope.Exclude(1)
			continue
		}
		snip := ""
		if h, err := c.call("conversations.history", map[string]string{"channel": id, "limit": "1"}); err == nil {
			if hs := asList(h["messages"]); len(hs) > 0 {
				m0, _ := hs[0].(map[string]any)
				snip = snippet(res.render(str(m0["text"])), 160)
			}
		}
		add(digest.DM, tsTime(str(im["latest"])), res.channel(id), "", snip, "", "")
	}

	// 3. Threads you're subscribed to (participated in) with recent activity.
	if r, err := c.call("subscriptions.thread.getView", map[string]string{"limit": "50"}); err == nil {
		for _, tv := range asList(r["threads"]) {
			t, _ := tv.(map[string]any)
			root, _ := t["root_msg"].(map[string]any)
			if root == nil {
				continue
			}
			if sub, _ := root["subscribed"].(bool); !sub {
				continue
			}
			if !w.allows(str(root["channel"]), "") {
				scope.Exclude(1)
				continue
			}
			latest := firstNonEmpty(str(root["latest_reply"]), str(root["ts"]))
			if tsTime(latest).Before(threadCut) {
				continue
			}
			rc := 0
			if v, ok := root["reply_count"].(float64); ok {
				rc = int(v)
			}
			unread := ""
			if lr := str(root["last_read"]); lr != "" && latest > lr {
				unread = ", unread"
			}
			lastWho := ""
			if reps := asList(t["latest_replies"]); len(reps) > 0 {
				lr, _ := reps[len(reps)-1].(map[string]any)
				lastWho = res.user(str(lr["user"]))
			}
			add(digest.Thread, tsTime(latest), "", res.channel(str(root["channel"])),
				`"`+snippet(res.render(str(root["text"])), 100)+`"`,
				fmt.Sprintf("%d replies, last @%s%s", rc, lastWho, unread), "")
		}
	}

	// 4. Remaining unread channels/mpims, mentions-first then most-recent.
	type uc struct {
		name     string
		mentions int
		latest   string
	}
	var ucs []uc
	for _, key := range []string{"channels", "mpims"} {
		for _, cv := range asList(counts[key]) {
			ch, _ := cv.(map[string]any)
			hu, _ := ch["has_unreads"].(bool)
			mc := 0
			if v, ok := ch["mention_count"].(float64); ok {
				mc = int(v)
			}
			if !hu && mc == 0 {
				continue
			}
			if !w.allows(str(ch["id"]), "") {
				scope.Exclude(1)
				continue
			}
			ucs = append(ucs, uc{res.channel(str(ch["id"])), mc, str(ch["latest"])})
		}
	}
	sort.Slice(ucs, func(i, j int) bool {
		if ucs[i].mentions != ucs[j].mentions {
			return ucs[i].mentions > ucs[j].mentions
		}
		return ucs[i].latest > ucs[j].latest
	})
	for _, u := range ucs {
		note := ""
		if u.mentions > 0 {
			note = fmt.Sprintf("%d mention%s", u.mentions, map[bool]string{true: "", false: "s"}[u.mentions == 1])
		}
		add(digest.Channel, time.Time{}, "", u.name, "", note, "")
	}

	return items
}

// Digest returns catch-up items across every signed-in workspace, or just the
// ones matching wsFilter. It feeds the cross-source `lurk summary`.
//
// Workspaces are gathered concurrently: each one costs several round-trips to
// Slack, and someone in six workspaces shouldn't wait six times as long for a
// digest. Results are reassembled in registry order so output stays stable.
func Digest(wsFilter string, mentionHours, threadHours int) ([]digest.Item, error) {
	reg, err := buildRegistry()
	if err != nil {
		return nil, err
	}

	var wanted []workspace
	for _, w := range reg {
		if wsFilter != "" {
			n := strings.ToLower(wsFilter)
			if !strings.Contains(strings.ToLower(w.Team), n) && !strings.Contains(strings.ToLower(w.URL), n) {
				continue
			}
		}
		wanted = append(wanted, w)
	}
	if len(wanted) == 0 && wsFilter != "" {
		return nil, fmt.Errorf("no workspace matching %q", wsFilter)
	}

	per := make([][]digest.Item, len(wanted))
	var wg sync.WaitGroup
	for i, w := range wanted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			per[i] = summaryItems(w, mentionHours, threadHours)
		}()
	}
	wg.Wait()

	var items []digest.Item
	for _, p := range per {
		items = append(items, p...)
	}
	return items, nil
}

// cmdSummary prints one workspace's catch-up digest.
func cmdSummary(w workspace, mentionHours, threadHours int) {
	digest.Render(os.Stdout, summaryItems(w, mentionHours, threadHours))
}

// cmdMentions prints a "did I already answer this?" digest: for each recent
// mention of `memberID` (default: the signed-in user), it expands the thread
// the mention lives in and reports whether the member has since replied. This
// closes the gap where `summary`/`search` surface the mention but never the
// member's OWN reply one level down in the thread — so a mention that's already
// been handled looks identical to one that's still waiting.
func cmdMentions(w workspace, memberID string, hours, count int, asJSON bool) {
	c := w.c
	res := newResolver(c)
	if memberID == "" {
		memberID = w.UserID
	}
	now := time.Now()
	cut := now.Add(-time.Duration(hours) * time.Hour)

	// Find mentions of the member. `after:` is date-granular, so widen a day and
	// filter precisely below (mirrors cmdSummary).
	q := fmt.Sprintf("<@%s> after:%s", memberID, cut.Add(-24*time.Hour).Format("2006-01-02"))
	r, err := c.call("search.messages", map[string]string{"query": q, "count": strconv.Itoa(count), "sort": "timestamp"})
	if err != nil {
		fail(err)
	}
	mm, _ := r["messages"].(map[string]any)
	var mentions []map[string]any
	for _, mv := range scopeMatches(w, asList(mm["matches"])) {
		m, _ := mv.(map[string]any)
		// Skip the member's own messages and anything older than the window.
		if str(m["user"]) == memberID || tsTime(str(m["ts"])).Before(cut) {
			continue
		}
		mentions = append(mentions, m)
	}
	sort.Slice(mentions, func(i, j int) bool { return str(mentions[i]["ts"]) > str(mentions[j]["ts"]) })

	type digest struct {
		Code        int    `json:"code,omitempty"`
		MentionTS   string `json:"mention_ts"`
		ChannelID   string `json:"channel_id"`
		ChannelName string `json:"channel"`
		MentionedBy string `json:"mentioned_by"`
		MentionText string `json:"mention_text"`
		Permalink   string `json:"permalink"`
		ThreadTS    string `json:"thread_ts"`
		ReplyCount  int    `json:"reply_count"`
		LatestTS    string `json:"latest_ts"`
		LatestWho   string `json:"latest_reply_by"`
		Answered    bool   `json:"answered_by_member"`
		AnswerTS    string `json:"answer_ts,omitempty"`
		Unavailable bool   `json:"thread_unavailable,omitempty"`
	}

	seen := map[string]bool{} // dedup by channel|thread_ts (same thread, multiple mentions)
	var out []digest
	for _, m := range mentions {
		ch, _ := m["channel"].(map[string]any)
		cid := str(ch["id"])
		mts := str(m["ts"])
		// conversations.replies needs the thread ROOT ts, not a reply's ts (a reply
		// ts returns only that one message). Search matches don't carry thread_ts as
		// a field, but the permalink does (?thread_ts=…); fall back to the message's
		// own ts when it's a top-level, non-threaded message.
		threadTS := threadRootFromPermalink(str(m["permalink"]), mts)

		d := digest{
			MentionTS:   mts,
			ChannelID:   cid,
			ChannelName: firstNonEmpty(str(ch["name"]), res.channel(cid)),
			MentionedBy: firstNonEmpty(str(m["username"]), res.user(str(m["user"])), "?"),
			MentionText: snippet(res.render(str(m["text"])), 200),
			Permalink:   str(m["permalink"]),
			ThreadTS:    threadTS,
		}
		var msgs []any
		if rr, err := c.call("conversations.replies", map[string]string{"channel": cid, "ts": threadTS, "limit": "200"}); err == nil {
			msgs = asList(rr["messages"])
		}
		if len(msgs) == 0 {
			d.Unavailable = true
		} else {
			d.ReplyCount = len(msgs) - 1
			for _, xv := range msgs {
				x, _ := xv.(map[string]any)
				xts := str(x["ts"])
				if xts > d.LatestTS {
					d.LatestTS = xts
					d.LatestWho = res.user(str(x["user"]))
				}
				// "answered" = the member spoke in this thread AFTER being mentioned.
				if str(x["user"]) == memberID && xts > mts {
					d.Answered = true
					if d.AnswerTS == "" || xts < d.AnswerTS {
						d.AnswerTS = xts
					}
				}
			}
		}

		key := cid + "|" + d.ThreadTS
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, d)
	}

	// Unanswered float to the top (that's the signal the coordinator acts on),
	// newest-first within each group.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Answered != out[j].Answered {
			return !out[i].Answered
		}
		return out[i].MentionTS > out[j].MentionTS
	})

	// Assign codes in display order so the printed [n] climb top-to-bottom.
	for i := range out {
		out[i].Code = assignCode(locator{
			Workspace: w.Team, Channel: out[i].ChannelID,
			ThreadTS: out[i].ThreadTS, Permalink: out[i].Permalink,
		})
	}

	if asJSON {
		printJSON(map[string]any{"member": memberID, "member_name": res.user(memberID), "hours": hours, "mentions": out})
		return
	}

	fmt.Printf("🔔 Mentions of @%s in %s (last %dh) — %d thread(s)\n\n", res.user(memberID), w.Team, hours, len(out))
	if len(out) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, d := range out {
		status := "⚠️  UNANSWERED"
		if d.Answered {
			status = "✅  answered by @" + res.user(memberID)
		} else if d.Unavailable {
			status = "❔  thread unavailable"
		}
		fmt.Printf("  %s   [%s] #%s  by @%s\n", status, fmtTS(d.MentionTS), d.ChannelName, d.MentionedBy)
		fmt.Printf("      \"%s\"\n", d.MentionText)
		switch {
		case d.Unavailable:
			fmt.Printf("      thread: (couldn't expand — free-tier gate or deleted)\n")
		case d.Answered:
			fmt.Printf("      answered at %s; thread: %d repl%s, last @%s at %s\n",
				fmtTS(d.AnswerTS), d.ReplyCount, plural(d.ReplyCount), d.LatestWho, fmtTS(d.LatestTS))
		default:
			fmt.Printf("      thread: %d repl%s, last @%s at %s\n",
				d.ReplyCount, plural(d.ReplyCount), d.LatestWho, fmtTS(d.LatestTS))
		}
		if tag, pl := codeTag(d.Code), d.Permalink; tag != "" || pl != "" {
			fmt.Printf("      %s%s\n", tag, pl)
		}
		fmt.Println()
	}
}

// threadRootFromPermalink pulls the thread_ts query param out of a Slack message
// permalink (present when the message lives in a thread); returns fallback (the
// message's own ts) for a top-level message that starts no thread.
func threadRootFromPermalink(permalink, fallback string) string {
	if u, err := url.Parse(permalink); err == nil {
		if t := u.Query().Get("thread_ts"); t != "" {
			return t
		}
	}
	return fallback
}

// normalizeTS accepts a Slack timestamp in any of the forms that appear in
// lurk's own output and returns the decimal thread_ts the API wants. It handles
// the permalink id form `p1785126651515049` (the `p`-prefixed, dot-less digits
// printed in search/history) by splitting off the trailing 6 microsecond
// digits; already-decimal ids pass through untouched.
func normalizeTS(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "p")
	if strings.Contains(s, ".") {
		return s
	}
	if len(s) > 6 && isAllDigits(s) {
		return s[:len(s)-6] + "." + s[len(s)-6:]
	}
	return s
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parsePermalink pulls the workspace host, channel ID, and thread root ts out of
// a Slack message permalink like
// https://team.slack.com/archives/C08JW756EUT/p1785126651515049?thread_ts=…
// The `?thread_ts=` query param, when present, is the true thread root — more
// reliable than the path's own p-id, which is the clicked message (a reply's id
// would open the wrong thread). ok is false if it isn't a usable message link.
func parsePermalink(raw string) (host, channel, threadTS string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if p == "archives" && i+1 < len(parts) {
			channel = parts[i+1]
			if i+2 < len(parts) {
				threadTS = normalizeTS(parts[i+2])
			}
		}
	}
	if t := u.Query().Get("thread_ts"); t != "" {
		threadTS = t
	}
	return u.Host, channel, threadTS, channel != "" && threadTS != ""
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// ScopeList prints what the scope actually admits, resolved against the live
// workspaces. This is the check that catches a typo: a channel named in the
// config that matches nothing here simply won't appear.
func ScopeList(out io.Writer) error {
	reg, err := buildRegistry()
	if err != nil {
		return err
	}
	for _, w := range reg {
		if w.c.chans == nil {
			fmt.Fprintf(out, "slack  %s — every channel\n", w.Team)
			continue
		}
		var names []string
		cursor := ""
		for {
			r, err := w.c.call("conversations.list", map[string]string{
				"types": "public_channel,private_channel,mpim,im", "limit": "1000", "cursor": cursor,
			})
			if err != nil {
				return err
			}
			for _, chv := range asList(r["channels"]) {
				ch, _ := chv.(map[string]any)
				id, nm := str(ch["id"]), str(ch["name"])
				if w.allows(id, nm) {
					names = append(names, fmt.Sprintf("#%s (%s)", firstNonEmpty(nm, id), id))
				}
			}
			if cursor = nextCursor(r); cursor == "" {
				break
			}
		}
		sort.Strings(names)
		fmt.Fprintf(out, "slack  %s — %s\n", w.Team, strings.Join(names, ", "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// command dispatch
// ---------------------------------------------------------------------------

// Usage is the help text for `lurk slack`.
const Usage = `lurk slack — read-only access to the Slack workspaces you're signed into.

usage:
  lurk [--json] slack workspaces
  lurk slack summary <workspace> [--mentions-hours 24] [--threads-hours 8]
  lurk [--json] slack mentions <workspace> [--user U…] [--hours 48] [--count 50]
  lurk [--json] slack channels <workspace> [--types t] [--filter s]
  lurk [--json] slack history  <workspace> <#channel|ID> [--limit n] [--oldest ts] [--cursor c]
  lurk [--json] slack replies  <code|permalink> [--limit n]
  lurk [--json] slack replies  <workspace> <#channel|ID> <thread_ts> [--limit n]
  lurk [--json] slack search   <workspace> <query> [--count n]
  lurk [--json] slack file     <workspace> <fileID|url_private> [--out path]

<workspace> is a case-insensitive substring of the team name or URL.

search, history, and mentions tag each thread with a short [code]. Pass that
code to 'replies' to reopen the thread — no need to copy a thread_ts. Codes last
for the session; a full permalink works too (and across sessions).

'file' downloads an attachment's bytes (read-only GET). Pass a file ID from
message JSON, or a url_private/url_private_download URL. Without --out it writes
to the temp dir under the file's own name and prints the path; --out - streams
the bytes to stdout.
`

// Run executes one `lurk slack …` subcommand.
func Run(args []string, asJSON bool) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, Usage)
		os.Exit(2)
	}

	switch args[0] {
	case "workspaces", "ws":
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		cmdWorkspaces(reg, asJSON)

	case "channels":
		fs := flag.NewFlagSet("channels", flag.ExitOnError)
		types := fs.String("types", "public_channel,private_channel,mpim,im", "conversation types")
		filter := fs.String("filter", "", "substring filter on name")
		rest := parseSub(fs, args[1:], 1, "channels <workspace>")
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, rest[0])
		if err != nil {
			return err
		}
		cmdChannels(w, *types, *filter, asJSON)

	case "history":
		fs := flag.NewFlagSet("history", flag.ExitOnError)
		limit := fs.Int("limit", 50, "max messages")
		oldest := fs.String("oldest", "", "unix ts lower bound")
		cursor := fs.String("cursor", "", "pagination cursor")
		rest := parseSub(fs, args[1:], 2, "history <workspace> <#channel|ID>")
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, rest[0])
		if err != nil {
			return err
		}
		cmdHistory(w, rest[1], *limit, *oldest, *cursor, asJSON)

	case "replies":
		fs := flag.NewFlagSet("replies", flag.ExitOnError)
		limit := fs.Int("limit", 100, "max messages")
		// replies takes either a single locator (a session code from search/
		// history/mentions output, or a full Slack permalink) or the legacy
		// <workspace> <#channel|ID> <thread_ts> triple. Grab the leading
		// positionals ourselves so we can branch on how many there are.
		pos, flags := leadingPositionals(args[1:])
		fs.Parse(flags)
		wsHint, channel, threadTS, err := resolveRepliesTarget(pos)
		if err != nil {
			fmt.Fprintln(os.Stderr, "usage: replies <code> | <permalink> | <workspace> <#channel|ID> <thread_ts>")
			return err
		}
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, wsHint)
		if err != nil {
			return err
		}
		cmdReplies(w, channel, threadTS, *limit, asJSON)

	case "file":
		fs := flag.NewFlagSet("file", flag.ExitOnError)
		out := fs.String("out", "", "output path (default: temp dir under the file's name; \"-\" for stdout)")
		rest := parseSub(fs, args[1:], 2, "file <workspace> <fileID|url_private>")
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, rest[0])
		if err != nil {
			return err
		}
		cmdFile(w, rest[1], *out, asJSON)

	case "summary":
		fs := flag.NewFlagSet("summary", flag.ExitOnError)
		mh := fs.Int("mentions-hours", 24, "mentions look-back window (hours)")
		th := fs.Int("threads-hours", 8, "active-threads look-back window (hours)")
		rest := parseSub(fs, args[1:], 1, "summary <workspace>")
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, rest[0])
		if err != nil {
			return err
		}
		cmdSummary(w, *mh, *th)

	case "mentions":
		fs := flag.NewFlagSet("mentions", flag.ExitOnError)
		user := fs.String("user", "", "member ID to check mentions of (default: the signed-in user)")
		hours := fs.Int("hours", 48, "look-back window (hours)")
		count := fs.Int("count", 50, "max mention matches to expand")
		rest := parseSub(fs, args[1:], 1, "mentions <workspace>")
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, rest[0])
		if err != nil {
			return err
		}
		cmdMentions(w, *user, *hours, *count, asJSON)

	case "raw":
		// raw read-only passthrough: slack-read raw <workspace> <method> [k=v ...]
		// The method allowlist bounds *what* it calls, never *which* channel, so
		// under a scope it's refused rather than left as a silent hole.
		if err := scope.Refuse("slack raw"); err != nil {
			return err
		}
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: raw <workspace> <method> [k=v ...]")
			os.Exit(2)
		}
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, args[1])
		if err != nil {
			return err
		}
		params := map[string]string{}
		for _, kv := range args[3:] {
			if k, v, ok := strings.Cut(kv, "="); ok {
				params[k] = v
			}
		}
		r, err := w.c.call(args[2], params)
		if err != nil {
			return err
		}
		printJSON(r)

	case "search":
		fs := flag.NewFlagSet("search", flag.ExitOnError)
		count := fs.Int("count", 20, "max matches")
		rest := parseSub(fs, args[1:], 2, "search <workspace> <query>")
		reg, err := buildRegistry()
		if err != nil {
			return err
		}
		w, err := pick(reg, rest[0])
		if err != nil {
			return err
		}
		cmdSearch(w, rest[1], *count, asJSON)

	default:
		fmt.Fprint(os.Stderr, Usage)
		os.Exit(2)
	}
	return nil
}

// leadingPositionals splits the leading non-flag args from the rest, for
// subcommands whose positional count varies (Go's flag package stops at the
// first non-flag, so flags must trail the positionals regardless).
func leadingPositionals(args []string) (pos, rest []string) {
	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return args[:i], args[i:]
}

// resolveRepliesTarget turns `replies` positionals into a (workspace hint,
// channel, thread_ts). It accepts, in order of preference:
//   - one permalink   → workspace host, channel, and thread root from the URL;
//   - one session code → the locator cached when the thread was last listed;
//   - three args       → the legacy <workspace> <#channel|ID> <thread_ts>, with
//     the ts normalized so a pasted p-form id works too.
func resolveRepliesTarget(pos []string) (wsHint, channel, threadTS string, err error) {
	switch len(pos) {
	case 1:
		if isURL(pos[0]) {
			host, ch, ts, ok := parsePermalink(pos[0])
			if !ok {
				return "", "", "", fmt.Errorf("could not read channel and thread from permalink %q", pos[0])
			}
			return host, ch, ts, nil
		}
		if code, cerr := strconv.Atoi(pos[0]); cerr == nil {
			loc, ok := resolveCode(code)
			if !ok {
				return "", "", "", fmt.Errorf("no thread with code %d in this session — run search/history/mentions first, or pass the permalink", code)
			}
			return loc.Workspace, loc.Channel, loc.ThreadTS, nil
		}
		return "", "", "", fmt.Errorf("%q is neither a session code nor a permalink", pos[0])
	case 3:
		return pos[0], pos[1], normalizeTS(pos[2]), nil
	default:
		return "", "", "", fmt.Errorf("expected 1 (code or permalink) or 3 (workspace channel thread_ts) arguments, got %d", len(pos))
	}
}

// parseSub splits positional args (which precede flags) from flags, since Go's
// flag package stops at the first non-flag. It expects exactly `nPos` leading
// positionals, then parses the remainder as flags.
func parseSub(fs *flag.FlagSet, args []string, nPos int, help string) []string {
	if len(args) < nPos {
		fmt.Fprintln(os.Stderr, "usage:", help)
		os.Exit(2)
	}
	pos := args[:nPos]
	fs.Parse(args[nPos:])
	return pos
}
