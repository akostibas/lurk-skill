package signal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"

	"github.com/akostibas/lurk-skill/internal/macos"
)

// --- Signal Desktop local store (macOS) ---

func signalDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Signal")
}

// sqlcipherKey decrypts the SQLCipher DB key from Signal's config.json.
// Newer Signal Desktop stores an Electron safeStorage-encrypted key
// ("v10" + AES-128-CBC ciphertext); the AES key is derived from the
// "Signal Safe Storage" macOS Keychain secret via Chromium's OSCrypt scheme
// (PBKDF2-HMAC-SHA1, salt "saltysalt", 1003 iters, 16-byte key, IV = 16×0x20).
// checkInstalled reports a missing Signal Desktop in those terms, so the
// absence doesn't surface as a bare "no such file" naming an internal path.
func checkInstalled() error {
	dir := signalDir()
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Signal Desktop isn't installed (no %s)", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "sql", "db.sqlite")); errors.Is(err, os.ErrNotExist) {
		return errors.New("Signal Desktop is installed but has no message database — open it and link your phone")
	}
	return nil
}

func sqlcipherKey() (string, error) {
	if err := checkInstalled(); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(signalDir(), "config.json"))
	if err != nil {
		return "", fmt.Errorf("read config.json: %w", err)
	}
	var cfg struct {
		Key          string `json:"key"`
		EncryptedKey string `json:"encryptedKey"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("parse config.json: %w", err)
	}
	if cfg.Key != "" { // legacy plaintext key
		return cfg.Key, nil
	}
	if cfg.EncryptedKey == "" {
		return "", fmt.Errorf("config.json has neither key nor encryptedKey")
	}

	ct, err := hex.DecodeString(cfg.EncryptedKey)
	if err != nil {
		return "", fmt.Errorf("decode encryptedKey hex: %w", err)
	}
	if !bytes.HasPrefix(ct, []byte("v10")) {
		return "", fmt.Errorf("encryptedKey missing v10 prefix (got %x)", ct[:3])
	}
	ct = ct[3:]

	secret, err := keychainSecret()
	if err != nil {
		return "", err
	}
	aesKey := pbkdf2.Key(secret, []byte("saltysalt"), 1003, 16, sha1.New)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	if len(ct)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext not block-aligned")
	}
	iv := bytes.Repeat([]byte{0x20}, aes.BlockSize)
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pt, err = pkcs7Unpad(pt)
	if err != nil {
		return "", fmt.Errorf("decrypt key (wrong Keychain secret?): %w", err)
	}
	return string(pt), nil
}

func keychainSecret() ([]byte, error) {
	return macos.Secret("Signal Safe Storage")
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > aes.BlockSize || n > len(b) {
		return nil, fmt.Errorf("bad padding")
	}
	return b[:len(b)-n], nil
}

// openSignalDB copies the live DB (plus its -wal/-shm sidecars) to a private
// temp dir and opens the copy. Copying — rather than opening Signal's files in
// place — means we never contend with the running app's locks and always see
// WAL-committed data. Caller must call the returned cleanup func.
func openSignalDB() (*sqliteDB, func(), error) {
	key, err := sqlcipherKey()
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "lurk-signal-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }

	src := filepath.Join(signalDir(), "sql", "db.sqlite")
	dst := filepath.Join(tmp, "db.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := copyFile(src+suffix, dst+suffix); err != nil && suffix == "" {
			cleanup()
			return nil, nil, err // main db must exist; sidecars are optional
		}
	}

	db, err := openSQLCipher(dst, key)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return db, func() { db.Close(); cleanup() }, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// Usage is the help text for `lurk signal`.
const Usage = `lurk signal — read-only access to your local Signal Desktop messages (macOS)

Usage: lurk [--json] signal <command> [args]

Commands:
  conversations [--filter s] [--dms|--groups] [--limit n]   list conversations by recent activity
  history <conv> [--limit n] [--before rowid]               messages in a conversation (oldest→newest)
  search <query> [--conv c] [--count n] [-C n | -A n -B n]  substring search over message text
  summary [--hours n]                                       unread + recent-activity digest
  whoami                                                    your own phone / ACI
  raw <SELECT ...>                                          arbitrary read-only query (JSON out)

<conv> is a conversation id or a case-insensitive substring of a name or phone number.
--json (before the "signal" keyword) emits raw JSON instead of formatted text.

--hours bounds recent activity only. Unread conversations are always listed,
however long they have been sitting there, and are marked "outside the window"
when their last activity predates it.

search can show the messages around each hit, grep-style: -C/--context n lines
on each side, or -A/--after-context and -B/--before-context to set the sides
independently. With context, hits are grouped by conversation and the matched
line is marked ">".

A declared scope, if one is in force, bounds every command here — conversations
it doesn't name are unreadable, and 'raw' is refused outright. Run 'lurk scope'
for what applies and how to declare it.`

// popFlag removes "--name value" (or "--name" for bools) from args, returning
// the value ("" if absent) and the remaining args. Thin wrapper over popFlags
// for the common single-long-flag case.
func popFlag(args []string, name string, hasVal bool) (string, []string) {
	return popFlags(args, hasVal, "--"+name)
}

// popFlags removes the first occurrence of any of the given flag tokens from
// args, returning its value ("" if absent, "true" for a valueless bool flag)
// and the remaining args. Tokens are matched literally, so a flag with both a
// long and a short spelling (grep-style "-A"/"--after-context") is one call.
// Minimal and order-independent.
func popFlags(args []string, hasVal bool, tokens ...string) (string, []string) {
	is := func(a string) bool {
		for _, t := range tokens {
			if a == t {
				return true
			}
		}
		return false
	}
	out := args[:0:0]
	val := ""
	for i := 0; i < len(args); i++ {
		if is(args[i]) {
			if hasVal && i+1 < len(args) {
				val = args[i+1]
				i++
			} else {
				val = "true"
			}
			continue
		}
		out = append(out, args[i])
	}
	return val, out
}

// contextLines resolves grep's context flags for `signal search`: -C/--context
// sets lines on both sides, while -A/--after-context and -B/--before-context
// override their own side. Absent flags mean 0 (no surrounding context).
func contextLines(contextS, beforeS, afterS string) (before, after int) {
	ctx := atoiOr(contextS, 0)
	return atoiOr(beforeS, ctx), atoiOr(afterS, ctx)
}

func atoiOr(s string, def int) int {
	var n int
	if s == "" {
		return def
	}
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}

// Run executes one `lurk signal …` subcommand.
func Run(args []string, jsonOut bool) error {
	if len(args) == 0 {
		fmt.Println(Usage)
		return nil
	}
	cmd := args[0]
	args = args[1:]

	db, cleanup, err := openSignalDB()
	if err != nil {
		return err
	}
	defer cleanup()

	switch cmd {
	case "conversations", "conv", "list":
		filter, rest := popFlag(args, "filter", true)
		limitS, rest := popFlag(rest, "limit", true)
		dms, rest := popFlag(rest, "dms", false)
		groups, rest := popFlag(rest, "groups", false)
		kind := ""
		if dms == "true" {
			kind = "dms"
		} else if groups == "true" {
			kind = "groups"
		}
		err = cmdConversations(db, jsonOut, filter, kind, atoiOr(limitS, 40))

	case "history", "hist":
		limitS, rest := popFlag(args, "limit", true)
		beforeS, rest := popFlag(rest, "before", true)
		if len(rest) == 0 {
			return fmt.Errorf("history needs a conversation (name/phone/id)")
		}
		var before int64
		fmt.Sscanf(beforeS, "%d", &before)
		err = cmdHistory(db, jsonOut, rest[0], atoiOr(limitS, 40), before)

	case "search":
		convArg, rest := popFlag(args, "conv", true)
		countS, rest := popFlag(rest, "count", true)
		ctxS, rest := popFlags(rest, true, "-C", "--context")
		afterS, rest := popFlags(rest, true, "-A", "--after-context")
		beforeS, rest := popFlags(rest, true, "-B", "--before-context")
		if len(rest) == 0 {
			return fmt.Errorf("search needs a query")
		}
		before, after := contextLines(ctxS, beforeS, afterS)
		err = cmdSearch(db, jsonOut, strings.Join(rest, " "), convArg, atoiOr(countS, 25), before, after)

	case "summary":
		hoursS, _ := popFlag(args, "hours", true)
		err = cmdSummary(db, jsonOut, atoiOr(hoursS, 24))

	case "whoami":
		err = cmdWhoami(db, jsonOut)

	case "raw":
		if len(args) == 0 {
			return fmt.Errorf("raw needs a SQL query")
		}
		err = cmdRaw(db, strings.Join(args, " "))

	default:
		fmt.Println(Usage)
		return nil
	}
	return err
}
