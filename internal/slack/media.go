package slack

import (
	"fmt"
	"regexp"
	"strings"
)

// mediaLines describes the parts of a message that carry no text — uploaded
// files, image blocks, and attachment/unfurl previews. Without these the text
// renderers drop them silently, so a message that was mostly a chart reads as
// if it had no content at all.
//
// ws is the workspace name, used to print a runnable `lurk slack file` command
// for uploads (their url_private needs the session token, so a bare URL is a
// dead end for a reader).
func mediaLines(m map[string]any, ws string) []string {
	var out []string

	for _, fv := range asList(m["files"]) {
		f, _ := fv.(map[string]any)
		label := firstNonEmpty(str(f["title"]), str(f["name"]), "file")
		kind := firstNonEmpty(str(f["mimetype"]), str(f["filetype"]))
		if kind != "" {
			label += " (" + kind + ")"
		}
		if ref := firstNonEmpty(str(f["id"]), str(f["url_private"])); ref != "" {
			label += fmt.Sprintf(" — fetch with: lurk slack file %q %s", ws, ref)
		}
		out = append(out, "[file] "+label)
	}

	// Image blocks nest in several containers (section accessory, context
	// elements, top level), so walk rather than enumerate. Blocks whose image is
	// a slack_file are skipped — that upload already showed up under `files`.
	walkBlocks(m["blocks"], func(b map[string]any) {
		if str(b["type"]) != "image" {
			return
		}
		if u := str(b["image_url"]); u != "" {
			out = append(out, "[image] "+strings.TrimSpace(str(b["alt_text"])+" — "+u))
		}
	})

	for _, av := range asList(m["attachments"]) {
		a, _ := av.(map[string]any)
		// An app's attachment often carries all its content in its own blocks
		// (Better Stack incidents, most bot cards) with only a bare title on the
		// message — so fall through to the block text before giving up.
		label := firstNonEmpty(str(a["title"]), str(a["service_name"]))
		body := firstNonEmpty(str(a["text"]), blockText(a["blocks"]), str(a["fallback"]))
		if u := firstNonEmpty(str(a["image_url"]), str(a["title_link"]), str(a["from_url"]), str(a["thumb_url"])); u != "" {
			label = strings.TrimPrefix(label+" — "+u, " — ")
		}
		if line := strings.Trim(label+": "+body, ": "); line != "" {
			out = append(out, "[attachment] "+snippet(line, 400))
		}
	}

	return out
}

var mrkdwnLinkRe = regexp.MustCompile(`<[^>]*>|:[a-z0-9_+' -]+:`)

// isLinkChrome reports text that is nothing but links and emoji — the
// "⚠️ Incident 🌐 Monitor 💬 Comment" button rows bots append to every card.
// They repeat verbatim on every message and say nothing a reader can use, so
// they're dropped. Image URLs live on image blocks, not here, and are untouched.
func isLinkChrome(s string) bool {
	return strings.TrimSpace(mrkdwnLinkRe.ReplaceAllString(s, "")) == ""
}

// blockText pulls the human-readable text out of a Block Kit tree — section and
// context blocks only, so button labels and confirm dialogs stay out.
func blockText(v any) string {
	var parts []string
	walkBlocks(v, func(b map[string]any) {
		switch str(b["type"]) {
		case "section":
			if t, ok := b["text"].(map[string]any); ok {
				parts = append(parts, str(t["text"]))
			}
		case "context":
			for _, ev := range asList(b["elements"]) {
				e, _ := ev.(map[string]any)
				if t := str(e["text"]); !isLinkChrome(t) {
					parts = append(parts, t)
				}
			}
		}
	})
	return strings.TrimSpace(strings.Join(parts, " "))
}

// walkBlocks visits every map in a Block Kit tree (blocks, elements, accessory…).
func walkBlocks(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			walkBlocks(e, fn)
		}
	case map[string]any:
		fn(t)
		for _, e := range t {
			walkBlocks(e, fn)
		}
	}
}

// printMedia writes mediaLines indented under the message line it belongs to.
func printMedia(m map[string]any, ws, indent string) {
	for _, l := range mediaLines(m, ws) {
		fmt.Printf("%s%s\n", indent, l)
	}
}
