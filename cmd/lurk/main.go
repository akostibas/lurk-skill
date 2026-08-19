// Command lurk gives read-only visibility into the chat you're signed into on
// this Mac — Slack workspaces and Signal Desktop — from one place.
//
// It never sends, posts, reacts, or marks anything read. See the per-source
// packages for how each one reaches its data.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/akostibas/lurk-skill/internal/digest"
	"github.com/akostibas/lurk-skill/internal/scope"
	"github.com/akostibas/lurk-skill/internal/signal"
	"github.com/akostibas/lurk-skill/internal/slack"
)

// version is injected at build time via -ldflags -X main.version.
var version = "dev"

const usage = `lurk — read-only access to the chat you're signed into on this Mac.

usage:
  lurk summary [--hours n] [--workspace s] [--no-slack] [--no-signal]
                              catch-up digest across every source
  lurk slack  <command> […]   Slack workspaces (see: lurk slack)
  lurk signal <command> […]   Signal Desktop  (see: lurk signal)
  lurk scope                  what this run may read, and how to declare it

  --json      emit raw JSON instead of formatted text (before the command)
  --version   print the build version

--hours bounds recent activity. Anything still unread is listed however old it
is, marked "outside the window" when it predates one.

Scope: if a config file declares which workspaces, channels, and Signal
conversations lurk may read, every command is bound by it — flags can narrow it
further, never widen it. The file is $LURK_CONFIG if set, else
~/.config/lurk/scope; with neither, lurk reads everything you're signed into.
Set LURK_REQUIRE_SCOPE=1 to make an unresolvable config fatal instead.

Run 'lurk scope' to see the file format and what your config resolves to. It
prints a worked example when no config is in force, so it's the place to start
whether you're declaring a scope or checking one.

lurk only ever reads. There is no code path that posts, replies, reacts,
joins, or marks anything read.
`

func main() {
	args := os.Args[1:]
	asJSON, args := popBool(args, "--json")

	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// Scope is resolved before any command runs, so no read path can start
	// before the floor is known.
	if err := scope.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	var err error
	switch args[0] {
	case "summary":
		err = cmdSummary(args[1:], asJSON)
	case "scope":
		err = cmdScope()
	case "slack":
		err = slack.Run(args[1:], asJSON)
	case "signal":
		err = signal.Run(args[1:], asJSON)
	case "--version", "-version", "version":
		fmt.Println(version)
	case "--help", "-h", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	// Always report exclusions, including on the error path: "skipped" has to
	// stay distinguishable from "nothing waiting".
	scope.Report(os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// cmdScope shows which config applies and what it resolves to, checked against
// the live sources so a name that matches nothing is visible before a run.
func cmdScope() error {
	scope.Describe(os.Stdout)
	if !scope.Active() {
		return nil
	}
	fmt.Println("\nresolved against what you're signed into:")
	var failures []string
	if err := slack.ScopeList(os.Stdout); err != nil {
		failures = append(failures, "slack: "+err.Error())
	}
	if err := signal.ScopeList(os.Stdout); err != nil {
		failures = append(failures, "signal: "+err.Error())
	}
	for _, f := range failures {
		fmt.Fprintln(os.Stderr, "could not resolve "+f)
	}
	return nil
}

// cmdSummary is the headline command: one catch-up digest spanning every source.
//
// A source that fails (Slack signed out, Signal not installed) is reported on
// stderr and skipped rather than fatal — a digest missing one source still beats
// no digest. It's only an error if *nothing* could be read.
func cmdSummary(args []string, asJSON bool) error {
	hours := 24
	ws := ""
	var skipSlack, skipSignal bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--hours":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &hours)
			}
		case "--workspace":
			if i+1 < len(args) {
				i++
				ws = args[i]
			}
		case "--no-slack":
			skipSlack = true
		case "--no-signal":
			skipSignal = true
		default:
			return fmt.Errorf("summary: unknown option %q", args[i])
		}
	}
	if hours <= 0 {
		return fmt.Errorf("summary: --hours must be positive")
	}

	var items []digest.Item
	var failures []string
	ok := 0

	if !skipSlack {
		// Threads are noisier than mentions, so they get a shorter window —
		// matching `lurk slack summary`'s defaults.
		threadHours := hours / 3
		if threadHours < 1 {
			threadHours = 1
		}
		got, err := slack.Digest(ws, hours, threadHours)
		if err != nil {
			failures = append(failures, "slack: "+err.Error())
		} else {
			items = append(items, got...)
			ok++
		}
	}
	if !skipSignal {
		got, err := signal.Digest(hours)
		if err != nil {
			failures = append(failures, "signal: "+err.Error())
		} else {
			items = append(items, got...)
			ok++
		}
	}

	for _, f := range failures {
		fmt.Fprintln(os.Stderr, "skipped "+f)
	}
	if ok == 0 {
		return fmt.Errorf("no source could be read:\n  %s", strings.Join(failures, "\n  "))
	}

	if asJSON {
		return printJSON(items)
	}
	digest.Render(os.Stdout, items)
	return nil
}

// popBool removes a boolean flag from args wherever it appears, so `--json` can
// precede or follow the command name.
func popBool(args []string, name string) (bool, []string) {
	out := args[:0:0]
	found := false
	for _, a := range args {
		if a == name {
			found = true
			continue
		}
		out = append(out, a)
	}
	return found, out
}
