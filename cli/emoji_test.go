package cli

import (
	"encoding/json"
	"os"
	"testing"
)

// TestStringWidthEmoji replays string-width 4.2.3 and emoji-regex 8.0.0 results recorded from node into
// testdata/emoji.json.
func TestStringWidthEmoji(t *testing.T) {
	data, err := os.ReadFile("testdata/emoji.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		S        string `json:"s"`
		Replaced string `json:"replaced"`
		Width    int    `json:"width"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		if got := replaceEmoji(c.S); got != c.Replaced {
			bad++
			if bad < 10 {
				t.Errorf("replaceEmoji(%q) = %q, want %q", c.S, got, c.Replaced)
			}
		}
		if got := stringWidth(c.S); got != c.Width {
			bad++
			if bad < 10 {
				t.Errorf("stringWidth(%q) = %d, want %d", c.S, got, c.Width)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d mismatches in %d cases", bad, len(cases))
	}
}
