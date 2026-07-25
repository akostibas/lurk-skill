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

  --json      emit raw JSON instead of formatted text (before the command)
  --version   print the build version

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

	var err error
	switch args[0] {
	case "summary":
		err = cmdSummary(args[1:], asJSON)
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
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
