package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	gulp "github.com/gulpjs/gulp"
	"github.com/gulpjs/gulp/internal/nodepath"
)

// logger follows gulp-cli's to-console wiring of the gulplog levels: error goes to stderr, warn, info and debug to
// stdout, each line prefixed with a timestamp, filtered by the -L log level.
type logger struct {
	stdout, stderr *output
	level          float64 // 0 disables every level
	chalk          chalk
	colorTime      bool
	muted          func() bool
}

// newLogger configures the levels from the merged flags as toConsole does.
func newLogger(stdout, stderr *output, f *flags, c chalk, colorTime bool, muted func() bool) *logger {
	l := &logger{stdout: stdout, stderr: stderr, chalk: c, colorTime: colorTime, muted: muted}
	if truthyValue(f.tasksSimple) || truthyValue(f.tasksJSON) || truthyValue(f.help) || truthyValue(f.version) || truthyValue(f.silent) {
		return l
	}
	level := f.logLevel
	if !truthyValue(level) {
		level = 3.0
	}
	for _, n := range []float64{0, 1, 2, 3} {
		if jsGreater(level, n) {
			l.level = n + 1
		}
	}
	return l
}

func (l *logger) write(w *output, msg string) {
	if msg == "" || (l.muted != nil && l.muted()) {
		return
	}
	w.writeString(l.timestamp() + " " + msg + "\n")
}

// output is a stream a run writes to. It keeps the first write error (a closed pipe, a full disk); later writes
// are skipped and Run returns 1 at the end, as a Node.js process fails when its stdout cannot be written.
type output struct {
	w   io.Writer
	mu  sync.Mutex
	err error
}

// writeString writes s unless an earlier write failed.
func (o *output) writeString(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return
	}
	if _, err := io.WriteString(o.w, s); err != nil {
		o.err = fmt.Errorf("writing output: %w", err)
	}
}

// failed returns the first write error, nil when every write succeeded.
func (o *output) failed() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func (l *logger) error(msg string) {
	if l.level >= 1 {
		l.write(l.stderr, msg)
	}
}

func (l *logger) warn(msg string) {
	if l.level >= 2 {
		l.write(l.stdout, msg)
	}
}

func (l *logger) info(msg string) {
	if l.level >= 3 {
		l.write(l.stdout, msg)
	}
}

func (l *logger) debug(msg string) {
	if l.level >= 4 {
		l.write(l.stdout, msg)
	}
}

// timestamp is the default timestamp: "[HH:MM:SS]", the time magenta when colours are on (util.inspect's date
// style).
func (l *logger) timestamp() string {
	t := time.Now().Format("15:04:05")
	if l.colorTime {
		t = "\x1b[35m" + t + "\x1b[39m"
	}
	return "[" + t + "]"
}

// formatHrTime follows gulp-cli's format-hrtime: the duration in the largest unit it reaches, with up to two decimals
// below 10 units.
func formatHrTime(d time.Duration) string {
	units := []struct {
		name string
		size float64
	}{{"h", 3600e9}, {"min", 60e9}, {"s", 1e9}, {"ms", 1e6}, {"μs", 1e3}}
	nano := float64(d.Nanoseconds())
	for _, u := range units {
		if nano < u.size {
			continue
		}
		if nano >= u.size*10 {
			return strconv.FormatFloat(jsRound(nano/u.size), 'f', -1, 64) + " " + u.name
		}
		s := strconv.FormatFloat(jsRound(nano*1e2/u.size), 'f', -1, 64)
		switch {
		case strings.HasSuffix(s, "00"):
			s = s[:len(s)-2]
		case strings.HasSuffix(s, "0"):
			s = s[:len(s)-2] + "." + s[len(s)-2:len(s)-1]
		default:
			s = s[:len(s)-2] + "." + s[len(s)-2:]
		}
		return s + " " + u.name
	}
	if nano > 0 {
		return strconv.FormatFloat(nano, 'f', -1, 64) + " ns"
	}
	return ""
}

// jsRound is Math.round: halves round towards positive infinity.
func jsRound(f float64) float64 {
	return math.Floor(f + 0.5)
}

// homeDir returns the home directory as os.homedir() finds it: $HOME when set, otherwise the user database
// entry, or the error of that lookup (os.homedir() throws then).
func homeDir() (string, error) {
	if h, ok := os.LookupEnv("HOME"); ok {
		return h, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}
	return u.HomeDir, nil
}

// tildify follows gulp-cli's tildify (replace-homedir with "~"): p under home is abbreviated to "~/...".
func tildify(p, home string) string {
	if !nodepath.IsAbs(p) {
		return p
	}
	if !nodepath.IsAbs(home) {
		return p
	}
	lookupHome := nodepath.Normalize(home + "/")
	lookupPath := nodepath.Normalize(p + "/")
	if !strings.HasPrefix(lookupPath, lookupHome) {
		return p
	}
	return nodepath.Normalize(strings.Replace(p, lookupHome, "~/", 1))
}

// Default messages of gulp-cli's translate.js.

func messageUsage(c chalk) string {
	return "\n" + c.paint(styleBold, "Usage:") + " gulp " + c.paint(styleBlue, "[options]") + " tasks"
}

func messageGulpfile(c chalk, home, path string) string {
	return "Using gulpfile " + c.paint(styleMagenta, tildify(path, home))
}

func messageCwdChanged(c chalk, home, cwd string) string {
	return "Working directory changed to " + c.paint(styleMagenta, tildify(cwd, home))
}

func messageDescription(c chalk, home, path string) string {
	return "Tasks for " + c.paint(styleMagenta, tildify(path, home))
}

func messageTaskStart(c chalk, task string) string {
	return "Starting '" + c.paint(styleCyan, task) + "'..."
}

func messageTaskStop(c chalk, task string, d time.Duration) string {
	return "Finished '" + c.paint(styleCyan, task) + "' after " + c.paint(styleMagenta, formatHrTime(d))
}

func messageTaskFailure(c chalk, task string, d time.Duration) string {
	return "'" + c.paint(styleCyan, task) + "' " + c.paint(styleRed, "errored after") + " " + c.paint(styleMagenta, formatHrTime(d))
}

func messageTaskMissing(c chalk, task string, similar []string) string {
	if len(similar) > 0 {
		return c.paint(styleRed, "Task never defined: "+task+" - did you mean? "+strings.Join(similar, ", ")) +
			"\nTo list available tasks, try running: gulp --tasks"
	}
	return c.paint(styleRed, "Task never defined: "+task) + "\nTo list available tasks, try running: gulp --tasks"
}

func messageMissingGulpfile(c chalk) string {
	return c.paint(styleRed, "No gulpfile found")
}

func messageCompletionUnknown(name string) string {
	return `echo "gulp autocompletion rules for '` + name + `' not found"`
}

// errorText is the first line(s) of the stack JavaScript prints for an Error with this message.
func errorText(err error) string {
	if _, ok := err.(*gulp.AssertionError); ok {
		return "AssertionError [ERR_ASSERTION]: " + err.Error()
	}
	msg := err.Error()
	if msg == "" {
		return "Error"
	}
	return "Error: " + msg
}
