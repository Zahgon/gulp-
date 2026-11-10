package cli

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/gulpjs/gulp/internal/jsnum"
)

// encodeValue mirrors the JSON encoding of yargs values used in testdata/argv.json.
func encodeValue(v any) any {
	switch x := v.(type) {
	case undefinedValue:
		return map[string]any{"u": true}
	case float64:
		switch {
		case math.IsNaN(x):
			return map[string]any{"n": "NaN"}
		case x == 0 && math.Signbit(x):
			return map[string]any{"n": "-0"}
		}
		return map[string]any{"n": jsnum.Format(x)}
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = encodeValue(e)
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := map[string]any{}
		for _, k := range keys {
			o[k] = encodeValue(x[k])
		}
		return map[string]any{"o": o}
	}
	return v
}

// TestArgvDifferential replays yargs 16 parses of gulp-cli's options recorded from node into
// testdata/argv.json.
func TestArgvDifferential(t *testing.T) {
	data, err := os.ReadFile("testdata/argv.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Args  []string       `json:"args"`
		Error *string        `json:"error"`
		Flags map[string]any `json:"flags"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	keys := []string{"help", "version", "preload", "gulpfile", "cwd", "tasks", "tasksSimple", "tasksJson",
		"tasksDepth", "compactTasks", "sortTasks", "silent", "continue", "series", "logLevel", "completion", "_"}
	bad := 0
	for _, c := range cases {
		argv, perr := parseArgv(c.Args)
		wantErr := ""
		if c.Error != nil {
			wantErr = *c.Error
		}
		if perr != wantErr {
			bad++
			if bad <= 20 {
				t.Errorf("%q: error %q, want %q", c.Args, perr, wantErr)
			}
			continue
		}
		for _, k := range keys {
			v, ok := argv[k]
			if !ok {
				v = undefined
			}
			got, err := json.Marshal(encodeValue(v))
			if err != nil {
				t.Fatal(err)
			}
			expected, recorded := c.Flags[k]
			if !recorded {
				expected = map[string]any{"u": true}
			}
			want, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			var g, w any
			if json.Unmarshal(got, &g) != nil || json.Unmarshal(want, &w) != nil {
				t.Fatal("re-decoding failed")
			}
			if !reflect.DeepEqual(g, w) {
				bad++
				if bad <= 20 {
					t.Errorf("%q: %s = %s, want %s", c.Args, k, got, want)
				}
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d mismatches in %d argument vectors", bad, len(cases))
	}
}
