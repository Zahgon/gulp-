// Package eventloop is the single-threaded scheduler every task callback runs on. It follows the Node.js event
// loop, whose ordering guarantees gulp's task logs depend on.
//
// One goroutine (the loop goroutine) runs macrotasks in FIFO order; after each macrotask it drains the nextTick
// queue, including ticks queued by ticks, exactly as Node.js drains process.nextTick callbacks between two
// callbacks of the event loop. Work arriving from other goroutines (a task's done callback called from a timer
// goroutine, a file watcher poll) is posted as a macrotask, while work started on the loop goroutine itself runs
// synchronously, so a task that calls done before returning continues its parent composition immediately, as it
// does in JavaScript.
//
// The loop also keeps a reference count of the handles that keep a Node.js process alive (pending timers, open
// file watchers) so a command line run can wait for them, and it can be halted to model process.exit: queued work
// is dropped and nothing runs until Resume.
package eventloop

import (
	"runtime"
	"sync"
	"time"
)

// loop is the process-wide scheduler state.
type loop struct {
	mu      sync.Mutex
	queue   []func()      // macrotasks waiting for the loop goroutine
	wake    chan struct{} // signalled when queue gains an entry
	gid     uint64        // goroutine id of the loop goroutine
	halted  bool          // set by Halt; posted work is discarded until Resume
	refs    int           // live handles (loop goroutine only)
	idle    []func()      // callbacks waiting for refs to reach zero (loop goroutine only)
	ticks   []func()      // the nextTick queue (loop goroutine only)
	thrown  bool          // the running tick threw (loop goroutine only)
	onHalt  []func()      // run once by the next Halt (guarded by mu)
	onEnd   []func()      // run once by the next EndRun (guarded by mu)
	gen     uint64        // incremented by Halt; timers of an earlier generation never fire (guarded by mu)
	started sync.Once
}

var std = &loop{wake: make(chan struct{}, 1)}

// start launches the loop goroutine once.
func (l *loop) start() {
	l.started.Do(func() {
		ready := make(chan struct{})
		go func() {
			l.gid = currentGoroutineID()
			close(ready)
			l.run()
		}()
		<-ready
	})
}

// run is the body of the loop goroutine.
func (l *loop) run() {
	for {
		l.mu.Lock()
		for len(l.queue) == 0 {
			l.mu.Unlock()
			<-l.wake
			l.mu.Lock()
		}
		fn := l.queue[0]
		l.queue[0] = nil
		l.queue = l.queue[1:]
		halted := l.halted
		l.mu.Unlock()
		if halted {
			continue
		}
		fn()
		l.drainTicks()
	}
}

// drainTicks runs queued nextTick callbacks until the queue is empty, a tick throws (see Throw) or the loop is
// halted.
func (l *loop) drainTicks() {
	for len(l.ticks) > 0 {
		if l.isHalted() {
			l.ticks = nil
			return
		}
		fn := l.ticks[0]
		l.ticks[0] = nil
		l.ticks = l.ticks[1:]
		fn()
		if l.thrown {
			l.thrown = false
			return
		}
	}
}

func (l *loop) isHalted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.halted
}

// post appends a macrotask.
func (l *loop) post(fn func()) {
	l.start()
	l.mu.Lock()
	if l.halted {
		l.mu.Unlock()
		return
	}
	l.queue = append(l.queue, fn)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// currentGoroutineID parses the id from the first line of the goroutine's stack trace ("goroutine N [...").
// Go deliberately has no public goroutine identity; the loop needs it only to tell whether a callback was
// invoked on the loop goroutine (run it now, as JavaScript would) or from elsewhere (queue it).
func currentGoroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	if n <= len(prefix) {
		return 0
	}
	var id uint64
	for _, c := range buf[len(prefix):n] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
	}
	return id
}

// OnLoop reports whether the caller runs on the loop goroutine.
func OnLoop() bool {
	std.start()
	return currentGoroutineID() == std.gid
}

// Post queues fn as a macrotask. It may be called from any goroutine.
func Post(fn func()) {
	std.post(fn)
}

// Dispatch runs fn immediately when called on the loop goroutine and posts it as a macrotask otherwise.
func Dispatch(fn func()) {
	if OnLoop() {
		if !std.isHalted() {
			fn()
		}
		return
	}
	std.post(fn)
}

// NextTick queues fn on the nextTick queue (process.nextTick). Called off the loop it posts a macrotask that
// queues the tick, so fn still runs on the loop goroutine.
func NextTick(fn func()) {
	if !OnLoop() {
		std.post(func() { std.ticks = append(std.ticks, fn) })
		return
	}
	std.ticks = append(std.ticks, fn)
}

// Throw records that the running nextTick callback ended with an exception that was handled (async-done's
// domain catching a task's throw). Node.js then abandons draining the tick queue: the remaining ticks run after
// the next macrotask, and it queues an empty immediate (setImmediate(noop)) so that happens soon. The loop
// reproduces both, which decides the order of the log lines that follow a throwing task. It must be called on
// the loop goroutine.
func Throw() {
	std.thrown = true
	std.post(func() {})
}

// Ref records a handle that keeps the process alive (a pending timer, an open watcher). It must be called on the
// loop goroutine.
func Ref() {
	std.refs++
}

// Unref releases a handle recorded by Ref and runs the idle callbacks when no handle is left. It must be called on
// the loop goroutine.
func Unref() {
	if std.refs > 0 {
		std.refs--
	}
	if std.refs == 0 {
		waiting := std.idle
		std.idle = nil
		for _, fn := range waiting {
			fn()
		}
	}
}

// WhenIdle runs fn on the loop goroutine once no handle recorded by Ref is left (immediately when none is). It
// must be called on the loop goroutine.
func WhenIdle(fn func()) {
	if std.refs == 0 {
		fn()
		return
	}
	std.idle = append(std.idle, fn)
}

// Halt stops the loop the way process.exit stops Node.js: queued macrotasks and ticks are dropped, work posted
// later is discarded, and live handles are forgotten, until Resume is called. It may be called from any goroutine.
func Halt() {
	std.start()
	std.mu.Lock()
	std.halted = true
	std.queue = nil
	std.gen++
	hooks := std.onHalt
	std.onHalt = nil
	std.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// OnHalt registers fn to run once at the next Halt, on the goroutine that calls Halt, or at once when the loop is
// halted already. Handles that own goroutines
// (file watchers) use it to stop them when the loop stops, as process.exit ends them in Node.js.
func OnHalt(fn func()) {
	std.mu.Lock()
	if std.halted {
		// The loop already stopped: nothing would run fn later, so it runs now.
		std.mu.Unlock()
		fn()
		return
	}
	std.onHalt = append(std.onHalt, fn)
	std.mu.Unlock()
}

// OnRunEnd registers fn to run once when a command line run ends normally (EndRun), on the goroutine that ends
// it. Handles that do not keep the process alive (watchers created with persistent false) use it to stop with the
// run, as the end of a Node.js process ends them.
func OnRunEnd(fn func()) {
	std.mu.Lock()
	std.onEnd = append(std.onEnd, fn)
	std.mu.Unlock()
}

// EndRun runs and clears the callbacks registered with OnRunEnd. It may be called from any goroutine.
func EndRun() {
	std.mu.Lock()
	hooks := std.onEnd
	std.onEnd = nil
	std.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// Resume lets the loop accept work again after Halt, clearing what the halted run left behind. On a loop that is
// not halted it does nothing, so live handles (open watchers) keep their references. It must not be called on the
// loop goroutine.
func Resume() {
	std.start()
	done := make(chan struct{})
	std.mu.Lock()
	if !std.halted {
		std.mu.Unlock()
		return
	}
	std.halted = false
	std.queue = append(std.queue, func() {
		std.ticks = nil
		std.refs = 0
		std.idle = nil
		close(done)
	})
	std.mu.Unlock()
	select {
	case std.wake <- struct{}{}:
	default:
	}
	<-done
}

// Timer is a setTimeout handle created by AfterFunc.
type Timer struct {
	t       *time.Timer
	gen     uint64 // the loop generation the timer belongs to
	stopped bool   // loop goroutine only
	fired   bool   // loop goroutine only
}

// generation returns the current loop generation.
func generation() uint64 {
	std.mu.Lock()
	defer std.mu.Unlock()
	return std.gen
}

// AfterFunc is setTimeout: fn runs on the loop goroutine after d, and the timer holds a reference (Ref) until it
// fires or is stopped. A Halt in between discards the timer, as process.exit discards pending timeouts. It must be
// called on the loop goroutine.
func AfterFunc(d time.Duration, fn func()) *Timer {
	tm := &Timer{gen: generation()}
	Ref()
	tm.t = time.AfterFunc(d, func() {
		std.post(func() {
			if tm.stopped || tm.fired || tm.gen != generation() {
				return
			}
			tm.fired = true
			fn()
			Unref()
		})
	})
	return tm
}

// Stop is clearTimeout. It must be called on the loop goroutine; stopping a fired or stopped timer does nothing.
func (tm *Timer) Stop() {
	if tm == nil || tm.stopped || tm.fired {
		return
	}
	tm.stopped = true
	tm.t.Stop()
	if tm.gen == generation() {
		// A timer of a halted run holds no reference any more (Resume cleared them).
		Unref()
	}
}
