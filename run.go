package gulp

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gulpjs/gulp/internal/eventloop"
)

// uidCounter numbers task runs across instances, as undertaker's module-level uid does (loop goroutine only).
var uidCounter int

// storage follows undertaker's per-run Storage: what the extensions need to emit events and record lastRun.
type storage struct {
	fn          *Task
	uid         int
	name        string
	branch      bool
	captureTime int64
	start       time.Time
}

// composition is one run of a series or parallel composition (bach over now-and-later).
type composition struct {
	g      *Gulp
	settle bool
	fns    []*Task
	done   Done
	called bool
	errs   []error // settle mode: each child's error by position (nil for success)
}

// finish calls the composition's callback once (now-and-later's once(done)).
func (c *composition) finish(err error) {
	if c.called {
		return
	}
	c.called = true
	if c.done != nil {
		c.done(err)
	}
}

// settledResult is the callback value of the settled form: nil when every child succeeded, otherwise a
// SettleError with the children's errors (bach's onSettled).
func (c *composition) settledResult() error {
	var errs []error
	for _, err := range c.errs {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return &SettleError{Errors: errs}
}

// series follows now-and-later's mapSeries.
func (c *composition) series() {
	var next func(i int)
	next = func(i int) {
		st := c.g.create(c.fns[i])
		c.g.before(st)
		asyncDone(c.fns[i], func(err error) {
			if err != nil && !c.settle {
				c.g.failed(err, st)
				c.finish(err)
				return
			}
			c.after(i, err, st)
			if i+1 >= len(c.fns) {
				c.finish(c.settledResult())
				return
			}
			next(i + 1)
		})
	}
	next(0)
}

// parallel follows now-and-later's map: every child is started (start event, task queued on the next tick) before
// any of them runs.
func (c *composition) parallel() {
	count := len(c.fns)
	for i, fn := range c.fns {
		st := c.g.create(fn)
		c.g.before(st)
		asyncDone(fn, func(err error) {
			if err != nil && !c.settle {
				c.g.failed(err, st)
				c.finish(err)
				return
			}
			c.after(i, err, st)
			count--
			if count == 0 {
				c.finish(c.settledResult())
			}
		})
	}
}

// after is the after extension; in settle mode a failed child arrives here as a settled error.
func (c *composition) after(i int, err error, st *storage) {
	if err != nil {
		if c.errs == nil {
			c.errs = make([]error, len(c.fns))
		}
		c.errs[i] = err
		c.g.failed(err, st)
		return
	}
	c.g.succeeded(st)
}

// asyncDone calls fn on the next tick with a done callback that reaches cb once, synchronously when it is
// called on the loop goroutine. A panic in fn fails it with the panic value.
func asyncDone(fn *Task, cb func(error)) {
	called := false
	finish := func(err error) {
		if called {
			return
		}
		called = true
		cb(err)
	}
	eventloop.NextTick(func() {
		done := Done(func(err error) {
			eventloop.Dispatch(func() { finish(err) })
		})
		defer func() {
			if r := recover(); r != nil {
				if called {
					// The panic came after done (or from the code done ran): async-done no longer listens
					// for the task's errors there, so it is not the task's failure.
					panic(r)
				}
				eventloop.Throw()
				finish(panicError(r))
			}
		}()
		fn.Fn(done)
	})
}

// panicError turns a recovered panic value into the error the task fails with: error values as they are,
// anything else as an error with its text (JavaScript's new Error(String(value))).
func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	if s, ok := r.(string); ok {
		return errors.New(s)
	}
	return errors.New(fmt.Sprint(r))
}

// create follows undertaker's Storage constructor.
func (g *Gulp) create(fn *Task) *storage {
	meta := fn.metadata()
	if meta == nil {
		buildTree(fn)
		meta = fn.metadata()
	}
	key := fn
	if meta.orig != nil {
		key = meta.orig
	}
	st := &storage{fn: key, uid: uidCounter, name: meta.name, branch: meta.branch, captureTime: nowMillis()}
	uidCounter++
	return st
}

// before emits the start event.
func (g *Gulp) before(st *storage) {
	st.start = time.Now()
	g.emit("start", &Event{UID: st.uid, Name: st.name, Branch: st.branch, Time: time.Now()})
}

// succeeded records lastRun and emits the stop event.
func (g *Gulp) succeeded(st *storage) {
	captureLastRun(st.fn, st.captureTime)
	g.emit("stop", &Event{UID: st.uid, Name: st.name, Branch: st.branch, Duration: time.Since(st.start), Time: time.Now()})
}

// failed releases lastRun and emits the error event with the first error of a settled list.
func (g *Gulp) failed(err error, st *storage) {
	if se, ok := err.(*SettleError); ok && len(se.Errors) > 0 {
		err = se.Errors[0]
	}
	releaseLastRun(st.fn)
	g.emit("error", &Event{UID: st.uid, Name: st.name, Branch: st.branch, Error: err, Duration: time.Since(st.start), Time: time.Now()})
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// lastRuns follows last-run's WeakMap from original task to capture time.
var lastRuns sync.Map

func captureLastRun(fn *Task, t int64) {
	lastRuns.Store(fn, t)
}

func releaseLastRun(fn *Task) {
	lastRuns.Delete(fn)
}

// LastRun returns when task last completed successfully, as a Unix time in milliseconds taken when that run
// started, rounded down to a multiple of resolution (0 means 1; a negative resolution rounds like its absolute
// value, as JavaScript's remainder does). task is a registered name or a *Task; a task registered under several
// names shares one record with the function it wraps. ok is false when the task has not completed yet (the
// original's undefined). An unknown name is the original's assertion error.
func (g *Gulp) LastRun(task any, resolution int) (int64, bool, error) {
	var fn *Task
	switch v := task.(type) {
	case string:
		fn = g.GetTask(v)
	case *Task:
		fn = v
	case TaskFunc, func(Done):
		if reflect.ValueOf(v).IsNil() {
			return 0, false, assertionError("Only functions can check lastRun")
		}
		return 0, false, nil
	}
	if fn == nil {
		return 0, false, assertionError("Only functions can check lastRun")
	}
	if m := fn.metadata(); m != nil && m.orig != nil {
		fn = m.orig
	}
	v, ok := lastRuns.Load(fn)
	if !ok {
		return 0, false, nil
	}
	t, isInt := v.(int64)
	if !isInt {
		return 0, false, nil
	}
	res := int64(resolution)
	if res == 0 {
		res = 1
	}
	return t - t%res, true, nil
}
