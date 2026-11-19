# lurk — product intent

## The problem

Chat is where work and life get coordinated, and it's spread across apps. Coming
back after a few hours means opening Slack, scrolling several workspaces for
anything addressed to you, then opening Signal and doing the same — and still
missing the one thread where someone asked you a question two replies deep.

Worse, an agent helping you can't see any of it. Ask Claude "did anyone need
anything from me today?" and it has no way to look.

## What lurk is for

**One window onto the chat you're already signed into on this Mac, for you and
for an agent working on your behalf — read-only unless you say otherwise.**

Its north star is a single question: *what's waiting for me, and what have I not
answered?* Everything else exists to support answering that.

Alongside that, one narrower thing: where the user has said so, an agent can
get this Mac onto a call that matters and off it again, so being present doesn't
depend on the user reaching for the app.

Commitments:

1. **Only reads, unless you declared otherwise — and never speaks for you.** No
   posting, replying, reacting, or marking read, ever. The single exception is
   being present on a call, and only in conversations the user has named in
   advance. Without that declaration it acts on nothing. This is what makes it
   safe to hand an agent: what it may touch is something you read, not something
   you trust. See [ADR-0002](adr/0002-signal-calls-by-declared-grant.md).
2. **No new credentials.** It reuses the sessions the user already has in the
   desktop apps. There is no bot to install, no workspace admin to ask, no API
   key to rotate. Adding a source must not mean adding a login.
3. **Ranked by who it's aimed at.** A digest is not a feed. Mentions of you beat
   unread DMs, which beat threads you're in, which beat ambient channel noise.
   Ordering *is* the product; without it you've rebuilt the unread badge.
4. **How much it can read is declared, not remembered.** A run's reach is a
   property of the tool, not of whoever invoked it. Where a config declares what
   may be read, every command is bound by it — flags narrow it, nothing widens
   it. Without that, an unattended agent's safety is a hope; with it, you can
   read a file and know. See [ADR-0001](adr/0001-declared-scope.md).

## Non-goals

- **Sending anything.** Replying belongs in the real client, where the user sees
  full context. Signal in particular would need a linked device via the Signal
  protocol — deliberately out of scope.
- **Taking part in a call.** Presence, not participation: it gets the Mac on and
  off a call. It doesn't speak, listen, record, or manage who else is there.
- **Being a client.** No live tailing, no notifications, no UI. It answers a
  question and exits.
- **Archiving or syncing.** It reads what the desktop apps already store
  locally. It does not build its own message store.
- **Other people's data.** It reads the signed-in user's own accounts. It is not
  a supervision or compliance tool.
- **Sanctioned integrations.** These are the user's own sessions, not official
  apps. If a platform ships a real read API that doesn't cost the user a
  workspace-admin conversation, prefer it — but not at the cost of commitment 2.

## What "working well" looks like

- After a few hours away, `lurk summary` tells the user everything that wants
  their attention across all sources, and nothing that doesn't.
- Nothing addressed to them personally is ever *below* ambient channel noise.
- A mention they already answered is visibly distinct from one still waiting.
- One source being unavailable (signed out, app not installed) degrades the
  digest, never fails it — and the user can tell "skipped" from "quiet".
- A stale or rotated session heals itself on the next run without the user
  learning anything about tokens.
- Two contexts on one machine — a person at a shell, an unattended agent working
  one engagement — hold different scopes, neither aware of the other, with
  nothing to pass per-invocation.
- Someone can answer "what will this run read?" by reading one file, or by
  running `lurk scope`, without running the command itself.
- Adding a fourth source means writing one `Digest()` function, not touching the
  other sources or the renderer.
- An agent can tell that a call has started, be on it within seconds, and be off
  it again, without the user touching the app — and nobody else is disturbed
  more than they would be by the user doing the same thing by hand.
- "Can't tell" is never reported as "no": when calls can't be seen, it says so.

## Signs it's malfunctioning

- The digest is long enough that the user skims it — ranking has stopped
  discriminating, or windows are too wide.
- The user still opens the app to find out whether they missed something.
- A source fails silently, or its emptiness is indistinguishable from its
  absence.
- Answered mentions keep resurfacing as if they were open.
- Reading requires the user to re-authenticate, approve prompts repeatedly, or
  think about tokens at all.
- Per-source flags have leaked into the cross-source command, so `lurk summary`
  needs source-specific knowledge to use.
- A read path exists that a declared scope doesn't bind, so what a run reads
  depends on which command the caller reached for.
- The effective scope is something you compute (merged files, layered defaults)
  rather than something you read.
- A conversation is dropped by scope without the run saying so, making an
  excluded digest look like a quiet one.
- It acts anywhere the user didn't name, or an action exists that the
  declaration doesn't bind.
- An action disturbs something already under way — cuts into a call in
  progress, rings people it was asked not to — or does more than it reported.
- It reports an action as done when the app didn't do it.
- The user has to re-grant, re-approve, or babysit each action.

## Where it could go

- More sources — iMessage, Discord, Telegram — each behind the same `Digest()`
  contract, each still credential-free.
- A "since I last looked" window instead of a fixed `--hours`.
- Ranking that learns which channels the user actually acts on.
- The unanswered-mention analysis (currently Slack-only) generalized to any
  source with threads.
