// Package gulp is the streaming build system: a task system (task registration, series and parallel
// composition, settle mode, last run times and the task tree), the file streams Src, Dest and Symlink, and Watch.
// Its behaviour follows gulp 5.0.1 with undertaker 2.0.0, bach 2.0.1, async-done 2.0.0, last-run 2.0.0 and
// glob-watcher 6.0.0.
//
// Task callbacks run on a single event-loop goroutine with a FIFO nextTick queue, reproducing the ordering of the
// original on Node.js: a composition emits a task's start event synchronously, runs the task function on the next
// tick, and continues synchronously when the task calls done on the loop goroutine. A Done called from another
// goroutine (a timer, an I/O goroutine) is queued on the loop. A panic inside a task function fails the task,
// the analogue of a thrown error.
package gulp

import (
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/gulpjs/gulp/vfs"
)

// Done is the completion callback a task receives: nil for success, an error for failure. Only the first call
// counts; later calls are ignored, as async-done's once wrapper does.
type Done func(err error)

// TaskFunc is a task function. It signals completion by calling done exactly once, synchronously or later from any
// goroutine.
type TaskFunc func(done Done)

// Task is a task: a function with the properties gulp reads from a JavaScript task function.
type Task struct {
	Fn          TaskFunc          // the task function
	DisplayName string            // fn.displayName: the name used by Gulp.Task("") and in trees and logs
	Description string            // fn.description, printed by gulp --tasks
	Flags       map[string]string // fn.flags, printed by gulp --tasks

	fnName string    // fn.name of a composition ("series", "parallel", ...)
	meta   *taskMeta // undertaker's metadata WeakMap entry
}

// taskMeta follows undertaker's per-function metadata.
type taskMeta struct {
	name   string
	branch bool
	orig   *Task
	tree   *Node
}

// metaMu guards Task.meta, the metadata undertaker keeps in a WeakMap: compositions, registration, runs and
// lastRun may read and write it from different goroutines.
var metaMu sync.RWMutex

// metadata returns t's metadata, nil when none was recorded yet.
func (t *Task) metadata() *taskMeta {
	metaMu.RLock()
	defer metaMu.RUnlock()
	return t.meta
}

// setMetadata records m as t's metadata.
func (t *Task) setMetadata(m *taskMeta) {
	metaMu.Lock()
	t.meta = m
	metaMu.Unlock()
}

// Unwrap returns the function a registered task wraps (undertaker's taskWrapper.unwrap()), or t itself when t
// is not a task returned by GetTask.
func (t *Task) Unwrap() *Task {
	if t == nil {
		return t
	}
	if m := t.metadata(); m != nil && m.orig != nil {
		return m.orig
	}
	return t
}

// Event is the payload of the start, stop and error events a Gulp instance emits for every task a composition
// runs (undertaker's events).
type Event struct {
	UID      int           // unique per run of a task
	Name     string        // the task's name, "<series>" or "<parallel>" for nested compositions
	Branch   bool          // true for compositions
	Error    error         // error events only: the first error when it is a settled list
	Duration time.Duration // stop and error events: time since start
	Time     time.Time     // when the event was emitted
}

// Gulp is a gulp instance (undertaker plus the vinyl-fs and watch methods). The zero value is usable and does
// not settle; New also reads UNDERTAKER_SETTLE.
type Gulp struct {
	mu        sync.Mutex
	registry  Registry
	settle    bool
	listeners map[string][]*listener
}

// listener is one registered event callback; its address identifies it for removal.
type listener struct {
	fn func(*Event)
}

// New returns a gulp instance with the default registry. Like undertaker's constructor it reads the
// UNDERTAKER_SETTLE environment variable: "true" makes compositions settle (run every task even after an error).
func New() *Gulp {
	return &Gulp{
		registry:  NewDefaultRegistry(),
		settle:    os.Getenv("UNDERTAKER_SETTLE") == "true",
		listeners: map[string][]*listener{},
	}
}

// SetSettle switches settle mode, the effect of UNDERTAKER_SETTLE=true. Compositions read the mode when they run,
// so a command line --continue applies to compositions built before the command line was parsed, as it does for a
// gulpfile loaded after gulp-cli set the variable.
func (g *Gulp) SetSettle(settle bool) {
	g.mu.Lock()
	g.settle = settle
	g.mu.Unlock()
}

func (g *Gulp) settled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.settle
}

// On registers a listener for the "start", "stop" or "error" event and returns a function that removes it.
// Listeners run on the loop goroutine in registration order.
func (g *Gulp) On(event string, fn func(*Event)) (remove func()) {
	l := &listener{fn: fn}
	g.mu.Lock()
	if g.listeners == nil {
		g.listeners = map[string][]*listener{}
	}
	g.listeners[event] = append(g.listeners[event], l)
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		list := g.listeners[event]
		for i, x := range list {
			if x == l {
				g.listeners[event] = append(list[:i:i], list[i+1:]...)
				return
			}
		}
	}
}

func (g *Gulp) emit(event string, e *Event) {
	g.mu.Lock()
	list := append([]*listener(nil), g.listeners[event]...)
	g.mu.Unlock()
	for _, l := range list {
		if l.fn != nil {
			l.fn(e)
		}
	}
}

// Registry returns the current registry.
func (g *Gulp) Registry() Registry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.registry == nil {
		g.registry = NewDefaultRegistry()
	}
	return g.registry
}

// AssertionError is an error the original raises with Node's assert module (task, registry and lastRun
// argument checks). The command line prints it as "AssertionError [ERR_ASSERTION]: <message>".
type AssertionError struct {
	Message string
}

// Error returns the assertion message.
func (e *AssertionError) Error() string {
	return e.Message
}

// assertionError returns a new AssertionError; every failure is a distinct value, as every throw is.
func assertionError(msg string) error {
	return &AssertionError{Message: msg}
}

// SetRegistry replaces the registry as undertaker's registry(newRegistry) does: every task of the current
// registry is set on the new one in order, then the new registry's Init is called with g. A nil registry returns
// the assertion error "Custom registry must have `get` function" and leaves the current one in place.
func (g *Gulp) SetRegistry(r Registry) error {
	if r == nil || isNilValue(r) {
		return assertionError("Custom registry must have `get` function")
	}
	for _, nt := range g.Registry().Tasks() {
		r.Set(nt.Name, nt.Task)
	}
	g.mu.Lock()
	g.registry = r
	g.mu.Unlock()
	r.Init(g)
	return nil
}

// isNilValue reports whether v holds a nil pointer, map, slice, func, channel or interface.
func isNilValue(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// Task registers task under name (undertaker's task(name, fn)). An empty name uses task.DisplayName (or the
// function name of a composition: "series", "parallel"). The registered task is a new wrapper around task, which
// GetTask returns; Unwrap gives task back. It returns the assertion error "Task name must be specified" when no
// name can be found and "Task function must be specified" when task or its Fn is nil; a later registration under
// the same name replaces the earlier one.
func (g *Gulp) Task(name string, task *Task) error {
	if name == "" && task != nil {
		name = task.DisplayName
		if name == "" {
			name = task.fnName
		}
	}
	if name == "" {
		return assertionError("Task name must be specified")
	}
	if task == nil || task.Fn == nil {
		return assertionError("Task function must be specified")
	}
	orig := task
	wrapper := &Task{DisplayName: name}
	wrapper.Fn = func(done Done) { orig.Fn(done) }
	var nodes []*Node
	if m := orig.metadata(); m != nil && m.branch {
		nodes = append(nodes, m.tree)
	}
	meta := &taskMeta{
		name: name,
		orig: orig,
		tree: &Node{Label: name, Type: "task", Nodes: nonNil(nodes)},
	}
	// The metadata is in place before the registry publishes the task.
	wrapper.setMetadata(meta)
	if stored := g.Registry().Set(name, wrapper); stored != nil && stored != wrapper {
		stored.setMetadata(meta)
	}
	return nil
}

// GetTask returns the task registered under name, nil when there is none (undertaker's task(name)). It never
// fails.
func (g *Gulp) GetTask(name string) *Task {
	return g.Registry().Get(name)
}

// Src returns a stream of the files matching globs (a string or []string; a leading "!" excludes) as vfs.Src
// does: files are buffered and decoded as utf8 by default, with base set to the glob parent. The error reports
// invalid globs or options; a singular glob that matches nothing fails later, from Wait, unless allowEmpty is set.
func (g *Gulp) Src(globs any, opts vfs.Options) (*vfs.Stream, error) {
	return vfs.Src(globs, opts)
}

// Dest returns a stream that writes every file below folder (a string or func(*vinyl.File) string) as vfs.Dest
// does, creating folders, keeping the file mode by default and overwriting existing files unless overwrite is
// false, then passes the file on with its new path. The error reports an invalid folder argument.
func (g *Gulp) Dest(folder any, opts vfs.Options) (*vfs.Stream, error) {
	return vfs.Dest(folder, opts)
}

// Symlink returns a stream that links every file into folder (a string or func(*vinyl.File) string) as
// vfs.Symlink does, with absolute link targets unless relativeSymlinks is set, then passes the file on with its
// new path and symlink target. The error reports an invalid folder argument.
func (g *Gulp) Symlink(folder any, opts vfs.Options) (*vfs.Stream, error) {
	return vfs.Symlink(folder, opts)
}

func nonNil(nodes []*Node) []*Node {
	if nodes == nil {
		return []*Node{}
	}
	return nodes
}
