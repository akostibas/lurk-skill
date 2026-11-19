# ADR-0002: lurk may act on Signal group calls, where a config line grants it

Status: Accepted (2026-11-19)

## Context

The goal: an agent working for the user should be able to notice that a Signal
group call has started, put this Mac on it, and take it off again.

That collides with lurk's first commitment, "never writes", which is what made
it safe to hand an agent. Three things were learned by experiment before
deciding (Signal Desktop 8.36, one real group, 2026-11-19):

- **The database can't say a call is live.** `callsHistory` records a call
  starting; all 175 rows on the test machine had a null end time. Signal's own
  log can: while a call runs, Desktop re-checks it every 1–2 minutes (and on
  every join/leave) and logs the connected-device count, and it logs its own
  connection state (lobby, joined, left) exactly. Ids are redacted to a short
  tail, which resolved uniquely among group conversations.
- **Signal can be driven through macOS accessibility.** Its buttons are
  reachable once Electron is asked to expose them. Reading the button tree
  through AppleScript/System Events took ~25s; through the AX C API, ~0.3s. A
  start took 1.4s end to end and a leave 0.7s.
- **Ringing is knowable before committing.** In Signal's code the lobby's ringing
  switch, its "will be notified / Signal will ring" line, and the ring decision
  at Start read one value. A lobby for a call that already has participants
  opens with ringing off, so joining can't ring anyone. A started call notifies
  the whole group even with ringing off; a group member described that as a
  "weird" ring. Signal's call log labels any call the desktop began as
  "ringing" regardless, so that label is not evidence either way.

## Decision

lurk gains one acting path: starting, joining and leaving **Signal group calls**
by pressing Signal Desktop's own buttons. Everything else stays read-only.

**Denied unless declared.** A `signal-calls <conversation>` line in the scope
config grants call actions in that one conversation. No line, no action; the
refusal names the exact line and file. Leaving is gated the same way, on the
conversation the desktop's call is in.

**The grant is separate from the read floor.** A file holding only
`signal-calls` lines leaves reading unbounded. ADR-0001's rule that an existing
file bounds reading is narrowed to: a file with read lines, or with nothing at
all, bounds reading. `LURK_REQUIRE_SCOPE=1` rejects a file that bounds none.

**Detection stays read-only** and needs no grant: it reads the log and the
database copy like every other read path.

**Quiet and non-disruptive by default.** Entering a call is muted, camera off,
ringing off unless flags say otherwise. Join and start are strict both ways
(join never starts, start never joins). Both refuse when the desktop is already
in a call or a lobby. An action only reports success once Signal's log confirms
the new state.

## Alternatives considered

**Keep lurk read-only; put call control in a separate tool.**
*Pro:* the "never writes" promise survives intact, with nothing to configure.
*Con:* detection and action need the same log parsing, conversation resolution
and scope, so the second tool would re-implement or import lurk; and an agent
would hold two tools whose safety story has to be read together anyway.

**A global on/off switch for actions.**
*Pro:* one line to understand. *Con:* a loose name match could then start a call
in, and notify, the wrong group. Per-conversation grants make the reach of an
action something you read, the same property ADR-0001 bought for reading.

**Let the grant line live under ADR-0001's rule unchanged** (any file bounds
reading).
*Pro:* no amendment to ADR-0001. *Con:* allowing calls would force everyone to
enumerate everything they read, which punishes the unscoped personal setup for
opting into an unrelated capability.

**AppleScript / System Events instead of the AX C API.**
*Pro:* no cgo, easy to read. *Con:* ~25s per look at the UI, several looks per
action, and its element matching failed silently three different ways while
prototyping.

**Speak the Signal protocol as a linked device** (signal-cli style), or a
deep link.
*Pro:* no dependence on button labels. *Con:* a linked device is a new
credential, breaking commitment 2; calls would need a media stack; and Signal's
`sgnl://` links reach call *links*, not group calls.

**Detect from the database alone.**
*Pro:* no log parsing. *Con:* it can't tell a live call from a finished one.

## Consequences

- "lurk never writes" becomes "lurk only reads unless a config line says
  otherwise". `docs/intent.md`, the README and the skill say so.
- Call actions need Accessibility permission for the app running lurk, and
  Signal Desktop running with its window open. Detection needs Signal running:
  a quit Signal notices no calls, which `calls` reports rather than "none".
- The button labels are Signal's English strings. A Signal update or another
  locale can break actions; they fail closed (nothing matching, nothing
  pressed) and `LURK_LIVE_CALLS=ui|lobby:<conv>` in `calls_test.go` is the
  diagnostic for it.
- Group calls only. 1:1 calls leave a different trail and are unexplored.
- Known ceilings: the chat must be visible in Signal's chat list; leave needs
  the call screen showing, not the floating mini-window; a group too large to
  ring is refused by start because the ringing switch is absent.
- Starting a call is never invisible to the group, whatever the flags.
