package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	gulp "github.com/gulpjs/gulp"
)

func TestFormatHrTime(t *testing.T) {
	// expectations printed by gulp-cli's format-hrtime.js
	cases := []struct {
		ns   int64
		want string
	}{
		{0, ""}, {1, "1 ns"}, {999, "999 ns"}, {1000, "1 μs"}, {1234, "1.23 μs"}, {9999, "10 μs"}, {10000, "10 μs"},
		{10500, "11 μs"}, {1500000, "1.5 ms"}, {15000000, "15 ms"}, {999999999, "1000 ms"}, {1000000000, "1 s"},
		{1500000000, "1.5 s"}, {9995000000, "10 s"}, {59999999999, "60 s"}, {60000000000, "1 min"},
		{61234000000, "1.02 min"}, {3599000000000, "60 min"}, {3600000000000, "1 h"}, {36000000000000, "10 h"},
		{1005, "1.01 μs"}, {1995, "2 μs"}, {2005, "2.01 μs"},
	}
	for _, c := range cases {
		if got := formatHrTime(time.Duration(c.ns)); got != c.want {
			t.Errorf("formatHrTime(%d) = %q, want %q", c.ns, got, c.want)
		}
	}
}

func TestTildify(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	cases := map[string]string{
		"/home/me/proj/gulpfile.js": "~/proj/gulpfile.js",
		"/home/me":                  "/home/me",
		"/home/me/":                 "~/",
		"/home/meow/x":              "/home/meow/x",
		"relative/path":             "relative/path",
		"/other":                    "/other",
	}
	for in, want := range cases {
		if got := tildify(in, "/home/me"); got != want {
			t.Errorf("tildify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestColorLevel(t *testing.T) {
	env := func(vars map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := vars[k]
			return v, ok
		}
	}
	cases := []struct {
		args []string
		env  map[string]string
		tty  bool
		want int
	}{
		{nil, nil, false, 0},
		{[]string{"--color"}, nil, false, 1},
		{[]string{"--color", "--no-color"}, nil, false, 0},
		{[]string{"--color=256"}, nil, false, 2},
		{[]string{"--color=16m"}, nil, false, 3},
		{[]string{"--", "--color"}, nil, false, 0},
		{nil, map[string]string{"FORCE_COLOR": "0"}, true, 0},
		{nil, map[string]string{"FORCE_COLOR": ""}, false, 1},
		{nil, map[string]string{"FORCE_COLOR": "7"}, false, 3},
		{nil, map[string]string{"FORCE_COLOR": "abc", "TERM": "xterm"}, false, 1},
		{nil, map[string]string{"TERM": "xterm-256color"}, true, 2},
		{nil, map[string]string{"TERM": "dumb"}, true, 0},
		{[]string{"--color"}, map[string]string{"TERM": "dumb"}, false, 1},
	}
	for _, c := range cases {
		if got := colorLevel(c.args, env(c.env), c.tty); got != c.want {
			t.Errorf("colorLevel(%q, %v, tty=%v) = %d, want %d", c.args, c.env, c.tty, got, c.want)
		}
	}
}

func TestChalk(t *testing.T) {
	on := chalk{on: true}
	cases := []struct{ in, want string }{
		{"", ""},
		{"x", "\x1b[36mx\x1b[39m"},
		{"a\x1b[39mb", "\x1b[36ma\x1b[39m\x1b[36mb\x1b[39m"},
		{"a\nb", "\x1b[36ma\x1b[39m\n\x1b[36mb\x1b[39m"},
		{"a\r\nb", "\x1b[36ma\x1b[39m\r\n\x1b[36mb\x1b[39m"},
	}
	for _, c := range cases {
		if got := on.paint(styleCyan, c.in); got != c.want {
			t.Errorf("cyan(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := (chalk{}).paint(styleCyan, "x"); got != "x" {
		t.Errorf("colour off: %q", got)
	}
}

func TestJSONString(t *testing.T) {
	cases := map[string]string{
		"plain":        `"plain"`,
		`q"b\`:         `"q\"b\\"`,
		"\n\t\x01\x7f": `"\n\t\u0001` + "\x7f" + `"`,
		"<series>":     `"<series>"`,
		"日本\u2028":     "\"日本\u2028\"",
	}
	for in, want := range cases {
		if got := jsonString(in); got != want {
			t.Errorf("jsonString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestHelpText(t *testing.T) {
	for file, c := range map[string]chalk{"testdata/help.txt": {}, "testdata/help-color.txt": {on: true}} {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := helpText(c) + "\n"; got != string(want) {
			t.Errorf("%s differs:\n%s", file, got)
		}
	}
}

var timestampRe = regexp.MustCompile(`\[\d\d:\d\d:\d\d\] `)
var durationRe = regexp.MustCompile(`after [0-9.]+ (h|min|s|ms|μs|ns)`)

// runFor runs the command line on g from a temporary folder (also HOME) holding the gulpfile and returns the
// exit code and the outputs with timestamps and durations removed.
func runFor(t *testing.T, g *gulp.Gulp, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("UNDERTAKER_SETTLE", "")
	t.Setenv("FORCE_COLOR", "0")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	gulpfile := filepath.Join(dir, "gulpfile.js")
	if err := os.WriteFile(gulpfile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r := &runner{g: g, stdout: &output{w: &stdout}, stderr: &output{w: &stderr}}
	code := r.run(gulpfile, args)
	clean := func(s string) string {
		return durationRe.ReplaceAllString(timestampRe.ReplaceAllString(s, ""), "after D")
	}
	return code, clean(stdout.String()), clean(stderr.String())
}

func newGulp(t *testing.T) *gulp.Gulp {
	t.Helper()
	g := gulp.New()
	a := &gulp.Task{DisplayName: "a", Description: "Task A", Flags: map[string]string{"--prod": "production"},
		Fn: func(done gulp.Done) { done(nil) }}
	b := &gulp.Task{DisplayName: "b", Fn: func(done gulp.Done) { time.AfterFunc(10*time.Millisecond, func() { done(nil) }) }}
	c := &gulp.Task{DisplayName: "c", Fn: func(done gulp.Done) { done(errors.New("boom")) }}
	for _, task := range []*gulp.Task{a, b, c} {
		if err := g.Task("", task); err != nil {
			t.Fatal(err)
		}
	}
	def, err := g.Series(a, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Task("default", def); err != nil {
		t.Fatal(err)
	}
	fail, err := g.Parallel("a", "c", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Task("fail", fail); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRun(t *testing.T) {
	t.Run("default task", func(t *testing.T) {
		code, out, errOut := runFor(t, newGulp(t))
		want := "Using gulpfile ~/gulpfile.js\nStarting 'default'...\nStarting 'a'...\nFinished 'a' after D\n" +
			"Starting 'b'...\nFinished 'b' after D\nFinished 'default' after D\n"
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
		}
	})
	t.Run("an error exits at once", func(t *testing.T) {
		code, out, errOut := runFor(t, newGulp(t), "fail")
		wantOut := "Using gulpfile ~/gulpfile.js\nStarting 'fail'...\nStarting 'a'...\nStarting 'c'...\nStarting 'b'...\n" +
			"Finished 'a' after D\n"
		wantErr := "'c' errored after D\nError: boom\n'fail' errored after D\n"
		if code != 1 || out != wantOut || errOut != wantErr {
			t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
		}
	})
	t.Run("continue settles", func(t *testing.T) {
		code, out, errOut := runFor(t, newGulp(t), "--continue", "fail")
		if code != 1 || !strings.Contains(out, "Finished 'b' after D") || !strings.Contains(errOut, "'fail' errored after D") {
			t.Fatalf("code %d\nstdout %q\nstderr %q", code, out, errOut)
		}
	})
	t.Run("missing task", func(t *testing.T) {
		code, _, errOut := runFor(t, newGulp(t), "defualt")
		want := "Task never defined: defualt - did you mean? default\nTo list available tasks, try running: gulp --tasks\n"
		if code != 1 || errOut != want {
			t.Fatalf("code %d stderr %q", code, errOut)
		}
	})
	t.Run("tasks tree", func(t *testing.T) {
		code, out, _ := runFor(t, newGulp(t), "--tasks", "--depth", "2")
		want := "Tasks for ~/gulpfile.js\n├── a        Task A\n│   --prod   …production\n├── b\n├── c\n" +
			"├─┬ default\n│ └── <series>\n└─┬ fail\n  └── <parallel>\n"
		if code != 0 || out != want {
			t.Fatalf("code %d\n%s", code, out)
		}
	})
	t.Run("tasks simple and json", func(t *testing.T) {
		if _, out, _ := runFor(t, newGulp(t), "--tasks-simple"); out != "a\nb\nc\ndefault\nfail\n" {
			t.Fatalf("tasks-simple %q", out)
		}
		_, out, _ := runFor(t, newGulp(t), "--tasks-json", "--compact-tasks")
		if !strings.HasPrefix(out, `{"label":"Tasks for ~/gulpfile.js","nodes":[{"label":"a","type":"task","nodes":[]}`) {
			t.Fatalf("tasks-json %q", out)
		}
	})
	t.Run("version, completion and argv errors", func(t *testing.T) {
		if code, out, _ := runFor(t, newGulp(t), "-v"); code != 0 || out != "CLI version: 3.1.0\nLocal version: 5.0.1\n" {
			t.Fatalf("version %d %q", code, out)
		}
		if code, out, _ := runFor(t, newGulp(t), "--completion=fish"); code != 0 || !strings.Contains(out, "complete -c gulp") {
			t.Fatalf("completion %d %q", code, out)
		}
		if code, out, _ := runFor(t, newGulp(t), "--completion=tcsh"); code != 5 || out != "echo \"gulp autocompletion rules for 'tcsh' not found\"\n" {
			t.Fatalf("unknown completion %d %q", code, out)
		}
		code, _, errOut := runFor(t, newGulp(t), "--gulpfile")
		if code != 1 || !strings.HasPrefix(errOut, "Not enough arguments following: gulpfile\n\nUsage: gulp [options] tasks\n") {
			t.Fatalf("argv error %d %q", code, errOut)
		}
	})
	t.Run("silent and log levels", func(t *testing.T) {
		if _, out, errOut := runFor(t, newGulp(t), "--silent", "fail"); out != "" || errOut != "" {
			t.Fatalf("silent printed %q %q", out, errOut)
		}
		_, out, errOut := runFor(t, newGulp(t), "-L", "fail")
		if out != "" || errOut != "'c' errored after D\nError: boom\n'fail' errored after D\n" {
			t.Fatalf("-L printed %q %q", out, errOut)
		}
	})
}
