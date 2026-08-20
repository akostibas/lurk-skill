package slack

import "testing"

// Slack paginates with response_metadata.next_cursor. Reading the wrong field
// returns "" — which every caller reads as "last page", so a listing silently
// stops after 200 users or 1000 channels instead of failing. A short user
// directory renders *wrong* names, not missing ones, so pin the field name.
func TestNextCursorReadsNextCursor(t *testing.T) {
	resp := map[string]any{
		"response_metadata": map[string]any{"next_cursor": "dXNlcjpVMEJERjBTRTBMOA=="},
	}
	if got := nextCursor(resp); got != "dXNlcjpVMEJERjBTRTBMOA==" {
		t.Errorf("next_cursor not read — pagination stops after page 1; got %q", got)
	}
}

func TestNextCursorEmptyOnLastPage(t *testing.T) {
	for name, resp := range map[string]map[string]any{
		"empty cursor": {"response_metadata": map[string]any{"next_cursor": ""}},
		"no metadata":  {},
	} {
		if got := nextCursor(resp); got != "" {
			t.Errorf("%s: want end of pagination, got %q", name, got)
		}
	}
}
