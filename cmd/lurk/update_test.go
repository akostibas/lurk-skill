package main

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.8.0", "v0.7.1", true},
		{"v0.7.10", "v0.7.9", true}, // numeric, not string, comparison
		{"v1.0.0", "v0.99.0", true},
		{"v0.7.1", "v0.7.1", false},
		{"v0.7.0", "v0.7.1", false},
		{"", "v0.7.1", false},
		{"v0.8.0", "v0.7.1-dirty", false}, // local builds never nag
		{"v0.8.0", "v0.7.1-3-gabc123", false},
		{"v0.8.0", "dev", false},
	}
	for _, c := range cases {
		if got := newer(c.latest, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}
