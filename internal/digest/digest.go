// Package digest is the shared vocabulary for catch-up summaries.
//
// Every source (Slack, Signal) turns its own notion of "what's waiting for you"
// into a flat list of Items, so `lurk summary` can merge sources into one view
// without knowing anything about how each source works.
package digest

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Kind is what a digest line represents. Sources use the kinds that fit them —
// Slack has mentions and threads; Signal only distinguishes unread from recent.
type Kind string

const (
	Mention Kind = "mention" // someone @-mentioned you
	DM      Kind = "dm"      // an unread direct message
	Thread  Kind = "thread"  // a thread you're in that moved
	Channel Kind = "channel" // an unread channel
	Unread  Kind = "unread"  // an unread conversation
	Recent  Kind = "recent"  // read, but active in the window
)

// kindOrder is the order sections print in: things aimed at you personally
// first, ambient activity last.
var kindOrder = []Kind{Mention, DM, Unread, Thread, Channel, Recent}

var kindLabel = map[Kind]string{
	Mention: "🔔 Mentions",
	DM:      "💬 Unread DMs",
	Unread:  "📬 Unread",
	Thread:  "🧵 Active threads",
	Channel: "📥 Other unread channels",
	Recent:  "🕒 Recent activity (already read)",
}

// Item is one line of a digest, from any source.
type Item struct {
	Source string    `json:"source"`          // "slack" | "signal"
	Scope  string    `json:"scope,omitempty"` // Slack workspace; empty for Signal
	Kind   Kind      `json:"kind"`
	When   time.Time `json:"when,omitzero"`
	Who    string    `json:"who,omitempty"`   // who spoke
	Where  string    `json:"where,omitempty"` // channel / conversation
	Text   string    `json:"text,omitempty"`  // message snippet
	Note   string    `json:"note,omitempty"`  // "3 unread", "unanswered", …
	Link   string    `json:"link,omitempty"`  // permalink, if the source has one
}

// group is one source+scope block of a rendered digest.
type group struct {
	source, scope string
	items         []Item
}

// Render writes items grouped by source (and Slack workspace), then by kind.
// Input order within a kind is preserved — sources sort their own sections.
func Render(w io.Writer, items []Item) {
	var groups []*group
	index := map[string]*group{}
	for _, it := range items {
		key := it.Source + "\x00" + it.Scope
		g, ok := index[key]
		if !ok {
			g = &group{source: it.Source, scope: it.Scope}
			index[key] = g
			groups = append(groups, g)
		}
		g.items = append(g.items, it)
	}

	if len(groups) == 0 {
		fmt.Fprintln(w, "Nothing waiting.")
		return
	}

	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(w)
		}
		title := strings.ToUpper(g.source)
		if g.scope != "" {
			title += " / " + g.scope
		}
		fmt.Fprintf(w, "═══ %s ═══\n", title)
		for _, k := range kindOrder {
			var section []Item
			for _, it := range g.items {
				if it.Kind == k {
					section = append(section, it)
				}
			}
			if len(section) == 0 {
				continue
			}
			fmt.Fprintf(w, "\n%s\n", kindLabel[k])
			for _, it := range section {
				fmt.Fprintln(w, "  "+it.line())
				if it.Link != "" {
					fmt.Fprintf(w, "      %s\n", it.Link)
				}
			}
		}
	}
}

// line renders a single item: "[when] where  who: text  (note)", skipping the
// parts this item doesn't have.
func (it Item) line() string {
	var b strings.Builder
	if !it.When.IsZero() {
		// Weekday+time reads best for a catch-up, but stops being unambiguous
		// once the window is longer than a week — then show the date.
		layout := "Mon 15:04"
		if time.Since(it.When) > 6*24*time.Hour {
			layout = "2006-01-02 15:04"
		}
		fmt.Fprintf(&b, "[%s] ", it.When.Format(layout))
	}
	if it.Where != "" {
		b.WriteString(it.Where)
	}
	if it.Who != "" {
		if it.Where != "" {
			b.WriteString("  ")
		}
		b.WriteString(it.Who)
		if it.Text != "" { // no dangling colon when there's nothing quoted
			b.WriteString(":")
		}
	}
	if it.Text != "" {
		b.WriteString(" ")
		b.WriteString(it.Text)
	}
	if it.Note != "" {
		fmt.Fprintf(&b, "  (%s)", it.Note)
	}
	return strings.TrimSpace(b.String())
}
