package digest

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Scripts reading --json treated a quiet window's null as an error (#11).
func TestEmptyListsEncodeAsBrackets(t *testing.T) {
	type row struct {
		Tags  []string `json:"tags"`
		Skip  []string `json:"skip,omitempty"`
		When  time.Time
		inner []string
	}
	var nilRows []row
	var nilAny []any
	cases := map[string]struct {
		in   any
		want string
	}{
		"top-level slice":   {nilRows, `[]`},
		"nested in a map":   {map[string]any{"mentions": nilRows, "n": 1}, `{"mentions":[],"n":1}`},
		"inside a struct":   {[]row{{}}, `[{"tags":[],"When":"0001-01-01T00:00:00Z"}]`},
		"nested any slices": {[]any{nilAny, map[string]any{"x": nilAny}}, `[[],{"x":[]}]`},
		"through a pointer": {&row{}, `{"tags":[],"When":"0001-01-01T00:00:00Z"}`},
		"nil map stays":     {map[string]any{"m": map[string]any(nil)}, `{"m":null}`},
		"bytes untouched":   {[]byte("hi"), `"aGk="`},
	}
	for name, c := range cases {
		b, err := json.Marshal(emptyLists(reflect.ValueOf(c.in)).Interface())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.TrimSpace(string(b)); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}

func TestEmptyListsLeavesInputAlone(t *testing.T) {
	in := map[string]any{"a": []any(nil)}
	emptyLists(reflect.ValueOf(in))
	if in["a"].([]any) != nil {
		t.Error("caller's data was mutated")
	}
}
