#!/usr/bin/env bash
#
# smoke-test.sh — end-to-end check that lurk can still reach the real local
# stores: Slack's desktop session and Signal's encrypted database.
#
# Unit tests cover formatting with fixtures; only this catches the things that
# actually break in practice — a Slack storage layout change, rotated tokens, a
# Keychain prompt, or a SQLCipher/Signal schema change.
#
# It reads your own messages (never prints their content) and writes nothing.
#
# Usage:
#   bin/smoke-test.sh              # both sources
#   bin/smoke-test.sh slack        # one source
#   bin/smoke-test.sh signal
#
# Exit codes: 0 = pass, 1 = setup failure, 2 = a source failed to read.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

want="${1:-both}"
case "$want" in
  both | slack | signal) ;;
  *) echo "FAIL: expected 'slack', 'signal', or no argument; got '$want'." >&2; exit 1 ;;
esac

[[ "$(uname)" == "Darwin" ]] || { echo "FAIL: lurk reads macOS app stores; this is $(uname)." >&2; exit 1; }

bindir="$(mktemp -d)"
trap 'rm -rf "$bindir"' EXIT

echo "Building lurk..."
ver="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
make --quiet "$(pwd)/internal/signal/sqlite3.c" 2>/dev/null || bin/vendor-sqlcipher.sh
go build -ldflags "-X main.version=$ver" -o "$bindir/lurk" ./cmd/lurk

# A freshly built binary has a new code identity, so macOS re-prompts for
# Keychain access once. That prompt is the usual reason a run appears to hang.
echo "Note: a new build prompts for Keychain access once — approve with 'Always Allow'."

fails=0

check() { # check <label> <count-of-output-lines-expected-at-least> <cmd...>
  local label="$1" min="$2"; shift 2
  local out
  if ! out="$("$@" 2>&1)"; then
    echo "FAIL: $label — command errored:" >&2
    echo "$out" | sed 's/^/      /' >&2
    fails=$((fails + 1))
    return
  fi
  local n
  n="$(printf '%s\n' "$out" | grep -c . || true)"
  if (( n < min )); then
    echo "FAIL: $label — expected at least $min lines of output, got $n." >&2
    fails=$((fails + 1))
    return
  fi
  echo "PASS: $label ($n lines)"
}

if [[ "$want" == "both" || "$want" == "slack" ]]; then
  check "slack workspaces"  1 "$bindir/lurk" slack workspaces
  check "slack summary"     1 "$bindir/lurk" summary --no-signal
fi

if [[ "$want" == "both" || "$want" == "signal" ]]; then
  check "signal conversations" 1 "$bindir/lurk" signal conversations --limit 5
  check "signal summary"       1 "$bindir/lurk" summary --no-slack
fi

if (( fails > 0 )); then
  echo "FAIL: $fails check(s) failed." >&2
  exit 2
fi
echo "PASS: all checks."
