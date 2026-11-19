# lurk

A Go CLI giving read-only-by-default access to the chat the user is signed into on this Mac
(Slack desktop app, Signal Desktop), plus a Claude Code skill (`skill/SKILL.md`)
that wraps it. See [docs/intent.md](docs/intent.md) for what it's for.

## Layout

- `cmd/lurk/` — the CLI: flag handling, source dispatch, and the cross-source
  `lurk summary`.
- `internal/digest/` — the shared vocabulary (`Item`, `Kind`) and the renderer
  every source's output flows through.
- `internal/slack/` — Slack: session extraction (LevelDB tokens + Keychain-
  decrypted cookies) and the Web API client, restricted to a read-only method
  allowlist.
- `internal/signal/` — Signal: SQLCipher key recovery and a minimal read-only
  cgo wrapper (`csqlite.go`) over the vendored amalgamation. Also group calls:
  detection from Signal's log, and the one acting path (`calls.go`, `axui.go`).
- `internal/scope/` — the config file: what may be read, and what may be acted on.
- `skill/` — `SKILL.md` (agent instructions) and `update.sh` (self-update:
  clones the latest release tag into `$TMPDIR` and reinstalls in place).
- `bin/` — workflow scripts: `vendor-sqlcipher.sh`, `smoke-test.sh`,
  `release.sh`.

**Adding a source** means adding a package with `Run(args, asJSON) error` and
`Digest(...) ([]digest.Item, error)`, then one case in `cmd/lurk`. If a change
requires the renderer or `cmd/lurk` to learn source-specific details, that's a
sign the `digest.Item` vocabulary needs widening instead.

## Read-only is the product, with one declared exception

Slack calls are gated by the `readMethods` allowlist; Signal only runs `SELECT`s
against a temp copy of the DB. Don't add a write path — the guarantee is what
makes the tool safe to hand an agent, and it's stated in the skill, the README,
and the intent doc.

The exception is Signal group calls (`internal/signal/calls.go`, `axui.go`),
which press Signal Desktop's buttons through macOS accessibility. Every acting
function must pass `mayCall` first: nothing is pressed in a conversation without
a `signal-calls` line for it. A second acting path needs its own ADR, not a
quiet addition — see [ADR-0002](docs/adr/0002-signal-calls-by-declared-grant.md).

Call actions match Signal's English button labels, so a Signal update can break
them. They fail closed. To see what Signal shows now without entering a call:
`LURK_LIVE_CALLS=ui` or `lobby:<conv>` with
`go test -count=1 ./internal/signal -run TestLiveCalls -v`.

## SQLCipher vendoring

Signal's DB needs SQLCipher, which only ships as source. `bin/vendor-sqlcipher.sh`
generates the amalgamation into `internal/signal/` (gitignored) from a pinned tag,
verified by SHA-256. Every build target depends on it, so a fresh clone just works.

Consequences: `go install …@latest` doesn't work (the cgo sources aren't in the
module) — install from a clone. Bumping SQLCipher means changing `VERSION` *and*
`TARBALL_SHA256` together.

## Testing

- `go test ./...` — unit tests, no Slack/Signal session required. The digest
  renderer is the part with real logic and is covered directly.
- `bin/smoke-test.sh [slack|signal]` — the one that matters: builds and reads
  the user's actual local stores. Run it after touching credential extraction,
  the Slack API surface, or Signal's schema assumptions, since those break from
  vendor changes rather than from our code.
- CI runs on macOS (Keychain, Apple Security framework) and skips anything
  needing a real session.

## Versioning & releases

SemVer, tagged `vMAJOR.MINOR.PATCH`. While pre-1.0, breaking changes bump the minor.

- **patch** — bug fixes, docs, CI, internal refactors (no behavior change).
- **minor** — new commands or flags, a new source, digest output changes.
- **major** — breaking changes to commands, flags, or JSON output shape.

Cut releases with `bin/release.sh <version|patch|minor|major>`. It refuses on a
dirty tree, wrong branch, out-of-sync `main`, an existing tag, or failing tests,
then runs the smoke test, tags, pushes, and creates a GitHub release. Never tag
by hand — the script is the gate that guarantees tests passed against the
published commit.

`lurk --version` reports the build version (injected via `-ldflags -X
main.version`).

## Privacy

This repo is about reading the user's private messages. Never commit fixtures,
logs, or test data containing real message content, contact names, workspace
names, tokens, or phone numbers — the repo is intended to be public.
