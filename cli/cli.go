// Package cli is the gulp command line, with the behaviour of gulp-cli 3.1.0: option parsing as its yargs 16
// configuration does it, .gulp.json config files, --tasks trees, --tasks-simple, --tasks-json, help, version,
// shell completion scripts, and the task run with the original's log lines, colours and exit codes.
package cli

import (
	"embed"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	gulp "github.com/gulpjs/gulp"
	"github.com/gulpjs/gulp/internal/eventloop"
	"github.com/gulpjs/gulp/internal/nodepath"
)

const (
	cliVersion   = "3.1.0"
	localVersion = "5.0.1"
)

//go:embed completion/bash completion/fish completion/powershell completion/zsh
var completions embed.FS

// flags are the parsed command line options merged with the config file's flags. Values are JavaScript values
// as the parser produces them (undefinedValue when absent).
type flags struct {
	help, version, preload, gulpfile, cwd any
	tasks, tasksSimple, tasksJSON         any
	tasksDepth, compactTasks, sortTasks   any
	silent, continueFlag, series          any
	logLevel, completion                  any
	positional                            []any
}

// flagsFrom reads the camel-cased keys gulp-cli reads from yargs' argv.
func flagsFrom(argv map[string]any) *flags {
	get := func(key string) any {
		if v, ok := argv[key]; ok {
			return v
		}
		return undefined
	}
	f := &flags{
		help: get("help"), version: get("version"), preload: get("preload"), gulpfile: get("gulpfile"),
		cwd: get("cwd"), tasks: get("tasks"), tasksSimple: get("tasksSimple"), tasksJSON: get("tasksJson"),
		tasksDepth: get("tasksDepth"), compactTasks: get("compactTasks"), sortTasks: get("sortTasks"),
		silent: get("silent"), continueFlag: get("continue"), series: get("series"), logLevel: get("logLevel"),
		completion: get("completion"),
	}
	if p, ok := argv["_"].([]any); ok {
		f.positional = p
	}
	return f
}

// setIfUndefined applies a config value to a flag the command line left undefined.
func (f *flags) setIfUndefined(name string, v any) {
	var target *any
	switch name {
	case "silent":
		target = &f.silent
	case "continue":
		target = &f.continueFlag
	case "series":
		target = &f.series
	case "logLevel":
		target = &f.logLevel
	case "compactTasks":
		target = &f.compactTasks
	case "tasksDepth":
		target = &f.tasksDepth
	case "sortTasks":
		target = &f.sortTasks
	default:
		return
	}
	if isUndefined(*target) {
		*target = v
	}
}

// Run runs the gulp command line on g with the given arguments (without the program name) and returns the exit
// code the original process would exit with. gulpfile is the gulpfile path to report, the file gulp-cli would
// find from the working directory; --gulpfile, --cwd and a config file's gulpfile key change it as they do in
// gulp-cli, and the process changes directory to the gulpfile's folder (or --cwd) when it differs, logging
// "Working directory changed to ...".
//
// Output goes to os.Stdout and os.Stderr. Run returns once the requested tasks have finished and nothing keeps
// the run alive (open watchers, pending watch runs); on the first error outside --continue it returns 1 at once
// and the rest of the run produces no output, as process.exit does. Run must not be called from a task.
func Run(g *gulp.Gulp, gulpfile string, args []string) int {
	r := &runner{g: g, stdout: &output{w: os.Stdout}, stderr: &output{w: os.Stderr}, stdoutTTY: isTerminal(os.Stdout)}
	code := r.run(gulpfile, args)
	if code == 0 && (r.stdout.failed() != nil || r.stderr.failed() != nil) {
		return 1
	}
	return code
}

// runner is one command line run.
type runner struct {
	g              *gulp.Gulp
	stdout, stderr *output
	stdoutTTY      bool
	home           string // the home directory paths are abbreviated against
	exited         atomic.Bool
}

// print writes s and a newline, as console.log does.
func (r *runner) print(w *output, s string) {
	w.writeString(s + "\n")
}

func (r *runner) run(gulpfile string, args []string) int {
	eventloop.Resume()
	argv, parseErr := parseArgv(args)
	level := colorLevel(args, os.LookupEnv, r.stdoutTTY)
	c := chalk{on: level > 0}
	colorTime := level != 0

	if parseErr != "" {
		r.print(r.stderr, parseErr)
		r.print(r.stderr, helpText(c))
		return 1
	}
	f := flagsFrom(argv)
	home, err := homeDir()
	if err != nil {
		r.print(r.stderr, errorText(err))
		return 1
	}
	r.home = home
	env, err := buildEnvironment(f, gulpfile, home)
	if err != nil {
		r.print(r.stderr, errorText(err))
		return 1
	}
	mergeConfigFlags(f, env.config[0])
	log := newLogger(r.stdout, r.stderr, f, c, colorTime, r.exited.Load)

	if truthyValue(f.completion) {
		return r.completion(f.completion)
	}
	if truthyValue(f.continueFlag) {
		if err := os.Setenv("UNDERTAKER_SETTLE", "true"); err != nil {
			r.print(r.stderr, errorText(err))
		}
		r.g.SetSettle(true)
	}
	if truthyValue(f.help) {
		r.print(r.stdout, helpText(c))
		return 0
	}
	if truthyValue(f.version) {
		r.print(r.stdout, "CLI version: "+cliVersion)
		r.print(r.stdout, "Local version: "+localVersion)
		return 0
	}
	if env.configPath == "" {
		log.error(messageMissingGulpfile(c))
		return 1
	}
	if wd, err := nodepath.Cwd(); err != nil || wd != env.cwd {
		if err := os.Chdir(env.cwd); err != nil {
			r.print(r.stderr, errorText(err))
			return 1
		}
		log.info(messageCwdChanged(c, r.home, env.cwd))
	}
	return r.execute(env, f, c, log)
}

// completion prints a completion script (exit 0) or the unknown-type message (exit 5).
func (r *runner) completion(name any) int {
	s, ok := name.(string)
	if !ok {
		r.print(r.stderr, "Error: Missing completion type")
		return 1
	}
	file := nodepath.Join("completion", s)
	if strings.Count(file, "/") == 1 {
		if data, err := completions.ReadFile(file); err == nil {
			r.print(r.stdout, string(data))
			return 0
		}
	}
	r.print(r.stdout, messageCompletionUnknown(s))
	return 5
}

// execute follows lib/versioned/^5.0.0/index.js.
func (r *runner) execute(env *environment, f *flags, c chalk, log *logger) int {
	switch {
	case truthyValue(f.tasksSimple):
		r.print(r.stdout, tasksSimple(r.g.Tree(false)))
		return 0
	case truthyValue(f.tasks):
		tree := r.g.Tree(true)
		tree.Label = messageDescription(c, r.home, env.configPath)
		for _, line := range logTasks(r.g, tree, f, c) {
			r.print(r.stdout, line)
		}
		return 0
	case truthyValue(f.tasksJSON):
		tree := r.g.Tree(true)
		tree.Label = messageDescription(c, r.home, env.configPath)
		out := tasksJSON(tree, f)
		if b, ok := f.tasksJSON.(bool); ok && b {
			r.print(r.stdout, out)
			return 0
		}
		if err := os.WriteFile(jsString(f.tasksJSON), []byte(out), 0o666); err != nil {
			r.print(r.stderr, errorText(err))
			return 1
		}
		return 0
	}
	return r.runTasks(env, f, c, log)
}

// runTasks runs the requested tasks on the event loop and waits for the run to end.
func (r *runner) runTasks(env *environment, f *flags, c chalk, log *logger) int {
	result := make(chan int, 1)
	var once sync.Once
	var removers []func()
	finish := func(code int) {
		once.Do(func() {
			for _, remove := range removers {
				remove()
			}
			result <- code
		})
	}
	exit := func(code int) {
		r.exited.Store(true)
		eventloop.Halt()
		finish(code)
	}
	eventloop.Post(func() {
		removers = r.logEvents(c, log)
		log.info(messageGulpfile(c, r.home, env.configPath))
		toRun := f.positional
		if len(toRun) == 0 {
			toRun = []any{"default"}
		}
		compose := r.g.Parallel
		if truthyValue(f.series) {
			compose = r.g.Series
		}
		task, err := compose(toRun)
		if err != nil {
			// gulp-cli tests err.task for truthiness, so the names 0 (a number on the command line) and ""
			// take the generic error path.
			var missing *gulp.TaskNotDefinedError
			if errors.As(err, &missing) && missing.Task != "" && missing.Task != "0" {
				log.error(messageTaskMissing(c, missing.Task, missing.Similar))
			} else {
				log.error(err.Error())
			}
			exit(1)
			return
		}
		task.Fn(func(err error) {
			if err != nil {
				exit(1)
				return
			}
			eventloop.WhenIdle(func() { finish(0) })
		})
	})
	return <-result
}

// logEvents follows lib/versioned/^5.0.0/log/events.js: start, stop and error lines, each error's text once.
func (r *runner) logEvents(c chalk, log *logger) []func() {
	var logged []error
	start := r.g.On("start", func(e *gulp.Event) {
		msg := messageTaskStart(c, e.Name)
		if e.Branch {
			log.debug(msg)
		} else {
			log.info(msg)
		}
	})
	stop := r.g.On("stop", func(e *gulp.Event) {
		msg := messageTaskStop(c, e.Name, e.Duration)
		if e.Branch {
			log.debug(msg)
		} else {
			log.info(msg)
		}
	})
	fail := r.g.On("error", func(e *gulp.Event) {
		msg := messageTaskFailure(c, e.Name, e.Duration)
		if e.Branch {
			log.debug(msg)
		} else {
			log.error(msg)
		}
		if e.Error == nil {
			return
		}
		for _, seen := range logged {
			if sameError(seen, e.Error) {
				return
			}
		}
		log.error(errorText(e.Error))
		logged = append(logged, e.Error)
	})
	return []func(){start, stop, fail}
}

// sameError compares errors by identity the way indexOf does for objects; values of incomparable types are
// never the same.
func sameError(a, b error) bool {
	ta := reflect.TypeOf(a)
	if ta != reflect.TypeOf(b) || !ta.Comparable() {
		return false
	}
	return a == b
}
