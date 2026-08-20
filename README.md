# lurk

One read-only window onto the chat you're already signed into on this Mac —
Slack workspaces and Signal Desktop — with a
[Claude Code](https://docs.claude.com/en/docs/claude-code/overview) skill that
wraps it.

`lurk` reads your own local sessions. It never sends, posts, reacts, joins, or
marks anything read.

```
$ lurk summary
═══ SLACK / Acme ═══

🔔 Mentions
  [Thu 21:29] #eng  dana: @you can you take a look at this before standup?
      https://acme.slack.com/archives/C0EXAMPLE01/p1700000000000000

📥 Other unread channels
  #product-bugs  (1 mention)
  #random

═══ SIGNAL ═══

📬 Unread
  [Fri 08:12] Mom  are we still on for sunday?  (2 unread)
```

## Setup

macOS only. Needs Go and, on first build, network access to fetch the pinned
SQLCipher sources.

```sh
git clone https://github.com/akostibas/lurk-skill
cd lurk-skill
make install            # builds the binary into ~/.claude/skills/lurk/
```

To use it from a terminal too:

```sh
ln -sf ~/.claude/skills/lurk/lurk ~/bin/lurk
```

The first run after each build triggers a macOS Keychain prompt (`Slack Safe
Storage`, `Signal Safe Storage`) — approve with **Always Allow**. A rebuilt
binary has a new code identity, so the prompt returns after upgrades.

Nothing to configure: whatever you're signed into in the Slack desktop app and
Signal Desktop is what `lurk` can see.

## Usage

```
lurk summary [--hours 24] [--workspace s] [--no-slack] [--no-signal]
lurk slack   <command> […]     # workspaces, summary, mentions, channels,
                               # history, replies, search, file, raw
lurk signal  <command> […]     # conversations, history, search, summary,
                               # whoami, raw
lurk scope                     # what this run is allowed to read
```

Run `lurk slack` or `lurk signal` with no arguments for that source's full
command list. `--json` anywhere in the command emits raw JSON.

**`lurk summary`** is the point of the tool: one catch-up across every source,
ordered by how personally addressed things are — mentions of you first, then
unread DMs and conversations, then threads you're in, then ambient unread
channels. A source that can't be read is reported on stderr and skipped, so one
signed-out app doesn't cost you the rest of the digest.

**`lurk slack mentions <ws>`** answers a question the digests can't: for each
recent mention it expands the whole thread and flags whether *you already
replied*, sorting unanswered mentions to the top.

## Limiting what a run can read

By default lurk reads every workspace and conversation you're signed into. To
bound it — most usefully for an unattended agent — declare a scope:

```
# ~/.config/lurk/scope   (or point $LURK_CONFIG anywhere)
slack Acme Corp              # the whole workspace
slack widgets/#eng           # just this channel
slack widgets/#eng-oncall
signal Team Chat             # by conversation name, id, or phone number
```

Every command is bound by it — flags can narrow further, never widen — and each
run reports on stderr how many results it excluded, so a filtered digest doesn't
look like a quiet one. Anything the file doesn't name is unreadable, including a
whole source: a Slack-only file excludes all of Signal.

`lurk scope` prints which file applies and resolves it against what you're
actually signed into, so a typo shows up as a channel that simply isn't there.

Exactly one file applies, no merging: `$LURK_CONFIG` if set, else
`~/.config/lurk/scope`, else nothing. Two contexts on one machine (you at a
shell, an agent on a schedule) get two files and neither knows about the other.

Set `LURK_REQUIRE_SCOPE=1` and lurk refuses to run unscoped — a typo'd
`LURK_CONFIG` becomes a loud failure instead of a digest that quietly includes
everything. Unattended callers should set both.

`slack raw` and `signal raw` take unbounded targets, so they're refused while a
scope is in force. Run them without `LURK_CONFIG` set if you mean to reach past
it. Rationale in [ADR-0001](docs/adr/0001-declared-scope.md).

Looking people up survives a scope: `lurk slack users <ws>` lists the whole
workspace directory, and `lurk --json slack workspaces` reports your own member
ID as `user_id`. A directory isn't channel content, so neither is scope-filtered
— otherwise a `<@U123>` from someone outside your channels would stay a raw ID.

## Two things to know

1. **Desktop sessions only.** Slack shows the workspaces signed into the desktop
   app (browser-only sessions don't count); Signal reads Signal Desktop, never
   your phone. Sign in there and it appears automatically.
2. **Free-tier Slack workspaces gate history.** History and search come back
   empty on a free plan even though channel and user metadata read fine. That's
   Slack's plan limit, not a bug.

## How it reads your messages

Slack: your per-team `xoxc-` token from the desktop app's Local Storage, plus
the `d` session cookie from the encrypted Cookies DB (decrypted with the Slack
Safe Storage Keychain key), used against Slack's Web API — restricted to an
allowlist of read-only methods.

Signal: the SQLCipher key from `config.json` (unwrapped via the Signal Safe
Storage Keychain secret), used to open a *copy* of `db.sqlite` for `SELECT`s
only, so it never contends with the running app.

Credentials stay in memory; the database copy is deleted on exit. Both use your
own account and your own local data — neither is a sanctioned integration, so
this is for personal use, not for pointing at anyone else's messages.

## Development

```sh
make build       # ./lurk
make test        # unit tests (no Slack/Signal session needed)
make vendor      # re-fetch the pinned SQLCipher sources
bin/smoke-test.sh   # live check against your real Slack + Signal
```

SQLCipher's amalgamation is generated by `bin/vendor-sqlcipher.sh` (pinned tag,
SHA-256 verified) rather than committed, so this repo carries no third-party C.
That's also why `go install …@latest` won't work — build from a clone.
[SQLCipher](https://www.zetetic.net/sqlcipher/) is © Zetetic LLC, under a
BSD-style license.

See [docs/intent.md](docs/intent.md) for what this tool is for, and
[CLAUDE.md](CLAUDE.md) for the development workflow.

## License

MIT
