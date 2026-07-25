---
name: lurk
description: Read-only access to the chat the user is signed into on this Mac — Slack workspaces and Signal Desktop. Catch-up digests across both, plus per-source listing, history, threads, and search. Use when the user asks you to look at, check, catch up on, summarize, or search their Slack or Signal (a channel, DM, thread, conversation, or workspace). Reads their own desktop sessions; never sends.
---

# lurk

Read-only visibility into the chat the user is signed into on this Mac:

- **Slack** — every workspace signed into the **Slack desktop app**. Reuses the
  user's own session (the per-team `xoxc-` token from Local Storage plus the `d`
  session cookie from the encrypted Cookies DB, decrypted with the "Slack Safe
  Storage" Keychain key) to call Slack's Web API.
- **Signal** — the local **Signal Desktop** store. Reads the SQLCipher-encrypted
  `db.sqlite` directly, with the DB key recovered from `config.json` via the
  "Signal Safe Storage" Keychain secret.

**Read-only by design.** Slack calls are restricted to an allowlist of read
methods; Signal only ever runs `SELECT`s against a *copy* of the database. No
code path posts, replies, reacts, joins, edits, deletes, or marks anything read.
Do not try to make it write — that is not what the user authorized.

## Running it

```
~/.claude/skills/lurk/lurk [--json] <command> [...]
```

If the binary is missing, reinstall from a clone: `make install`. (A rebuilt
binary has a new code identity, so macOS re-prompts for Keychain access once —
click **Always Allow**.)

## Commands

```
lurk summary [--hours 24] [--workspace s] [--no-slack] [--no-signal]

lurk slack workspaces
lurk slack summary  <ws> [--mentions-hours 24] [--threads-hours 8]
lurk slack mentions <ws> [--user U…] [--hours 48] [--count 50]
lurk slack channels <ws> [--filter s] [--types t]
lurk slack history  <ws> <#channel|ID> [--limit n] [--oldest ts] [--cursor c]
lurk slack replies  <ws> <#channel|ID> <thread_ts> [--limit n]
lurk slack search   <ws> <query> [--count n]
lurk slack raw      <ws> <method> [k=v ...]

lurk signal conversations [--filter s] [--dms|--groups] [--limit n]
lurk signal history <conv> [--limit n] [--before rowid]
lurk signal search  <query> [--conv c] [--count n]
lurk signal summary [--hours n]
lurk signal whoami
lurk signal raw     <SELECT ...>
```

- `--json` (anywhere in the command) emits raw JSON instead of formatted text.
- `<ws>` is a case-insensitive substring of a Slack workspace name or URL.
- `<conv>` is a Signal conversation id, or a substring of a contact/group name
  or phone number. Ambiguous substrings list the candidates.
- Slack channels can be `#name` or an ID (`C…`/`D…`/`G…`).

## Which command to reach for

**"Catch me up" / "anything I've missed?" → `lurk summary`.** It spans every
signed-in Slack workspace *and* Signal in one digest, ordered by how personally
addressed things are: mentions of the user first, then unread DMs and
conversations, then threads they're in, then ambient unread channels. Narrow it
with `--workspace` or `--no-signal` when the user names one place.

A source that can't be read (signed out, app not installed) is reported on
stderr and skipped, not fatal — check stderr before telling the user a source
was quiet, since "skipped" and "nothing waiting" mean very different things.

**"Did I answer X?" → `lurk slack mentions <ws>`.** For each recent mention it
expands the whole thread and flags whether the user has since replied, sorting
**unanswered mentions to the top**. `summary` and `search` surface the mention
but never the user's *own* reply one level down, so a handled mention looks
identical to a waiting one there — this command is the one that tells them apart.
Use `--user U…` to ask the same question about a teammate.

**Reading a specific conversation** → `slack history` / `slack replies` /
`signal history`. **Finding something** → `slack search` (Slack's own search
syntax) or `signal search` (substring over message text).

## Limitations worth knowing before you report a gap

1. **Desktop sessions only.** Slack shows only workspaces signed into the
   desktop *app* (browser-only sessions don't count); Signal reads only Signal
   Desktop, never a phone. If something the user expects is missing, that's the
   usual reason — they sign in on the desktop app and it appears automatically.
2. **Free-tier Slack workspaces gate history.** `conversations.history` and
   search return empty (`"is_limited": true`) even though channel and user
   metadata read fine. That's Slack's plan limit, not a failure — say so rather
   than reporting the channel as empty.
3. **Keychain access.** Reading the "Slack/Signal Safe Storage" secrets triggers
   a native macOS prompt on first run after a rebuild. In Claude Code this may
   also need a Bash permission rule for `security find-generic-password`.
4. **Signal search is substring, not full-text.** Signal's FTS index uses a
   custom tokenizer this engine can't load, so `search` scans message bodies
   with `LIKE` — predictable, but no stemming or ranking.

## Notes

- Credentials live in memory only; the Signal DB copy lands in a temp dir and is
  deleted on exit. Nothing is written to disk or logged.
- Slack tokens rotate when the user signs out or Slack forces re-auth; stale
  tokens are validated and dropped each run, so it self-heals.
- This reads the user's *own* accounts using their *own* local sessions. It is
  not a sanctioned Slack or Signal integration — fine for personal read-only
  use, not something to point at anyone else's data.
