package slack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMediaLines(t *testing.T) {
	const raw = `{
	  "text": "Your subscription is ready!",
	  "files": [{"id": "F123", "name": "chart.png", "mimetype": "image/png"}],
	  "blocks": [
	    {"type": "section", "accessory": {"type": "image", "alt_text": "Errors by type",
	      "image_url": "https://us.posthog.com/exporter/export-abc.png"}},
	    {"type": "image", "slack_file": {"id": "F123"}, "alt_text": "already listed as a file"}
	  ],
	  "attachments": [
	    {"title": "Docs page", "title_link": "https://example.com/doc"},
	    {"fallback": "[no preview available]", "blocks": [
	      {"type": "section", "text": {"type": "mrkdwn", "text": "*Cause:* Status 502"}},
	      {"type": "context", "elements": [
	        {"type": "mrkdwn", "text": ":warning: <https://uptime.example.com/incidents/1|Incident>"},
	        {"type": "mrkdwn", "text": ":globe_with_meridians: <https://uptime.example.com/monitors/1|Monitor>"}
	      ]},
	      {"type": "actions", "elements": [{"type": "button", "text": {"text": "Acknowledge"}}]}
	    ]}
	  ]
	}`
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}

	got := mediaLines(m, "Acme")
	want := []string{
		`[file] chart.png (image/png) — fetch with: lurk slack file "Acme" F123`,
		`[image] Errors by type — https://us.posthog.com/exporter/export-abc.png`,
		`[attachment] Docs page — https://example.com/doc`,
		`[attachment] *Cause:* Status 502`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if lines := mediaLines(map[string]any{"text": "just words"}, "Acme"); len(lines) != 0 {
		t.Errorf("plain message should produce no media lines, got %v", lines)
	}
}
