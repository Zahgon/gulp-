package cli

import (
	"math"
	"os"
	"regexp"
	"strings"

	"github.com/gulpjs/gulp/internal/jsnum"
)

// colorLevel follows supports-color 7.2.0's level for stdout. chalk 4 colours its styles when the level is positive;
// util.inspect colours the timestamp whenever it is not 0 (a negative FORCE_COLOR gives a negative level).
// args are the command line arguments (has-flag reads process.argv), getenv reads the environment and
// stdoutTTY tells whether stdout is a terminal.
func colorLevel(args []string, getenv func(string) (string, bool), stdoutTTY bool) int {
	hasFlag := func(flag string) bool {
		pos, term := -1, -1
		for i, a := range args {
			if pos < 0 && a == "--"+flag {
				pos = i
			}
			if term < 0 && a == "--" {
				term = i
			}
		}
		return pos != -1 && (term == -1 || pos < term)
	}
	// forceColor is undefined (forceSet false), NaN (forceNaN) or an integer.
	forceSet, forceNaN, force := false, false, 0
	if hasFlag("no-color") || hasFlag("no-colors") || hasFlag("color=false") || hasFlag("color=never") {
		forceSet, force = true, 0
	} else if hasFlag("color") || hasFlag("colors") || hasFlag("color=true") || hasFlag("color=always") {
		forceSet, force = true, 1
	}
	if v, ok := getenv("FORCE_COLOR"); ok {
		forceSet, forceNaN = true, false
		switch {
		case v == "true":
			force = 1
		case v == "false":
			force = 0
		case v == "":
			force = 1
		default:
			n := jsnum.ParseInt(v)
			if math.IsNaN(n) {
				forceNaN = true
			} else {
				force = int(math.Max(math.Min(n, 3), math.MinInt32))
			}
		}
	}
	if forceSet && !forceNaN && force == 0 {
		return 0
	}
	if hasFlag("color=16m") || hasFlag("color=full") || hasFlag("color=truecolor") {
		return 3
	}
	if hasFlag("color=256") {
		return 2
	}
	if !stdoutTTY && !forceSet {
		return 0
	}
	min := 0
	if forceSet && !forceNaN {
		min = force
	}
	env := func(k string) string {
		v, _ := getenv(k)
		return v
	}
	has := func(k string) bool {
		_, ok := getenv(k)
		return ok
	}
	term := env("TERM")
	if term == "dumb" {
		return min
	}
	if has("CI") {
		for _, sign := range []string{"TRAVIS", "CIRCLECI", "APPVEYOR", "GITLAB_CI", "GITHUB_ACTIONS", "BUILDKITE"} {
			if has(sign) {
				return 1
			}
		}
		if env("CI_NAME") == "codeship" {
			return 1
		}
		return min
	}
	if has("TEAMCITY_VERSION") {
		if teamcityRe.MatchString(env("TEAMCITY_VERSION")) {
			return 1
		}
		return 0
	}
	if env("COLORTERM") == "truecolor" {
		return 3
	}
	if has("TERM_PROGRAM") {
		version := jsnum.ParseInt(strings.Split(env("TERM_PROGRAM_VERSION"), ".")[0])
		switch env("TERM_PROGRAM") {
		case "iTerm.app":
			if version >= 3 {
				return 3
			}
			return 2
		case "Apple_Terminal":
			return 2
		}
	}
	if term256Re.MatchString(term) {
		return 2
	}
	if termBasicRe.MatchString(term) {
		return 1
	}
	if has("COLORTERM") {
		return 1
	}
	return min
}

var (
	teamcityRe  = regexp.MustCompile(`^(9\.(0*[1-9]\d*)\.|\d{2,}\.)`)
	term256Re   = regexp.MustCompile(`(?i)-256(color)?$`)
	termBasicRe = regexp.MustCompile(`(?i)^screen|^xterm|^vt100|^vt220|^rxvt|color|ansi|cygwin|linux`)
)

// isTerminal reports whether f is a character device, the stdlib-visible sign of a terminal.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// style is one chalk 4 style.
type style struct{ open, close string }

var (
	styleBold    = style{"\x1b[1m", "\x1b[22m"}
	styleBlue    = style{"\x1b[34m", "\x1b[39m"}
	styleGray    = style{"\x1b[90m", "\x1b[39m"}
	styleMagenta = style{"\x1b[35m", "\x1b[39m"}
	styleCyan    = style{"\x1b[36m", "\x1b[39m"}
	styleRed     = style{"\x1b[31m", "\x1b[39m"}
	styleWhite   = style{"\x1b[37m", "\x1b[39m"}
	styleYellow  = style{"\x1b[33m", "\x1b[39m"}
)

// chalk applies styles when colour is on.
type chalk struct{ on bool }

// paint follows chalk's applyStyle for a single style: a closing code inside s is followed by a re-opening one, and
// every line break is wrapped in close and open codes.
func (c chalk) paint(st style, s string) string {
	if !c.on || s == "" {
		return s
	}
	if strings.Contains(s, "\x1b") {
		s = strings.ReplaceAll(s, st.close, st.close+st.open)
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		var b strings.Builder
		end := 0
		for i >= 0 {
			cut := i
			nl := "\n"
			if i > 0 && s[i-1] == '\r' {
				cut = i - 1
				nl = "\r\n"
			}
			b.WriteString(s[end:cut])
			b.WriteString(st.close)
			b.WriteString(nl)
			b.WriteString(st.open)
			end = i + 1
			next := strings.IndexByte(s[end:], '\n')
			if next < 0 {
				break
			}
			i = end + next
		}
		b.WriteString(s[end:])
		s = b.String()
	}
	return st.open + s + st.close
}
