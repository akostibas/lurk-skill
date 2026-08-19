# ADR-0001: Readable scope is declared in a config file, enforced as a floor

Status: Accepted (2026-08-19)

## Context

lurk reads every Slack workspace the user is signed into and their entire Signal
store. Scope existed only as per-invocation flags, so how much of someone's
private life a run touched depended on the caller remembering to pass them.

That is awkward for a person — six signed-in workspaces, four of them social —
and disqualifying for an agent. A consumer building an hourly unattended pipeline
routed around `lurk summary` entirely, hard-coding channel IDs and group names
against `history`, because there was no way to point an unattended agent at the
digest without handing it everything. Their words: per-invocation scope makes
each run's safety "a hope, not a guarantee".

Two problems, one mechanism: *what* may be read (issue #3) and *which* list a
given invocation gets, on a machine where a person and one or more agents all
run lurk (issue #4).

## Decision

A config file declares what lurk may read, enforced as a **floor across every
read path**. Per-invocation flags narrow it further; nothing widens it. No config
file means today's behaviour, so the human catch-up case is untouched.

**Resolution is one file, not a merge.** `$LURK_CONFIG` if set, else
`~/.config/lurk/scope`, else nothing. One wins outright. Merging would make "what
can this run read" something you compute instead of something you read, and being
able to read it is the property being bought.

**Allowlist, not denylist**, on failure asymmetry. An allowlist miss silently
omits a new channel — recoverable, and it surfaces the next time a human mentions
it. A denylist miss reads a *new* personal conversation into a work digest that
may already have been summarized into a day log or posted somewhere. That one
can't be undone. The allowlist's friction is answered by reporting
`N results excluded by scope` on stderr, keeping "skipped" distinguishable from
"nothing waiting" — a property the tool already holds elsewhere.

**`LURK_REQUIRE_SCOPE=1` fails closed.** Discovery alone still fails open: a
typo'd path would silently fall back to reading everything. With this set, an
unresolvable config exits non-zero and reads nothing. The property is not "scope
is configured" but "*this caller* declared that it must be". Humans never set it.

**`raw` is refused while a scope is in force**, on both sources. Slack's method
allowlist bounds *what* is called, never *which* channel; `signal raw` takes an
arbitrary SELECT that can't be bound without parsing SQL. The escape hatch is
running that one command with `LURK_CONFIG` unset, which is visible in the
command line rather than buried in a file.

**`lurk scope` resolves the config against the live sources.** Channel IDs are
unreadable enough that a wrong-but-plausible config would go unnoticed; without a
way to see what a run will read before running it, "declarative scope" is a
promise nobody can check.

**Enforcement points.** Slack: workspaces are gated in `buildRegistry`, the one
place the registry is built; message-bearing calls (`conversations.history`,
`.replies`) are gated in `client.call` beside the existing read-only method
allowlist; and search, mentions, and the digest — where Slack picks the channels,
not the caller — are filtered on the way out. Signal: conversation resolution and
each list-producing query filter through one `keepInScope`.

## Alternatives considered

**Do nothing; keep per-invocation flags.**
*Pro:* no new concepts, no config file to get wrong. *Con:* the safety of a run
is a property of the caller, not the tool — which is precisely what lost the
motivated consumer.

**A default scope for `summary` only** (the original ask).
*Pro:* smallest change; fixes the reported symptom. *Con:* a default only changes
where the caller starts. If `signal history <anything>` still reads anything, the
caller is still the guarantee.

**Denylist ("never read these").**
*Pro:* no friction as new channels appear. *Con:* the failure mode is
unrecoverable, per the asymmetry above.

**Merge several config files (global + per-directory).**
*Pro:* a personal baseline plus per-project additions. *Con:* the effective scope
becomes something you compute. Rejected for the same reason as ordering: one file
wins outright.

**Walk up from the working directory for `.lurk.toml`** (proposed in #4).
*Pro:* an agent in a client repo needs nothing per-invocation. *Con:* needs a
documented stopping point, and a stray file above a home directory silently
captures unrelated runs — fails open in exactly the way this ADR is trying to
close. An agent can set `LURK_CONFIG` once just as easily. Rejected.

**TOML for the config format.**
*Pro:* comments, hand-editable, familiar. *Con:* a new dependency in a tool with
three, for a file that is a list of names. A line-oriented format (`source` then
rest-of-line) gets comments for free and needs no quoting for Signal
conversation names with spaces.

**Named profiles / per-pipeline presets.**
Rejected as a non-goal in #3 and still rejected: one declarative scope,
uniformly enforced. Two contexts get two files.

## Consequences

- A config naming only Slack excludes all of Signal, and vice versa. That's the
  allowlist working, and the stderr count is what makes it visible.
- `slack file` needs a file ID, not a `url_private` URL, while scoped: a URL
  carries no channel to check against. The file's own channels are checked.
- Channel-name resolution costs one extra `conversations.list` per workspace on
  the first content call, cached for the run.
- Metadata methods (`conversations.info`, `users.*`, `client.counts`) are not
  gated — only message-bearing ones. Gating them would make resolving a channel's
  name recurse through the check, and they return what `slack channels` already
  shows.
- `docs/intent.md` gains scope as a commitment; it changes what lurk is *for*,
  not just how it's built.
