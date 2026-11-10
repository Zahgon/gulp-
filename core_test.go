package gulp_test

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	gulp "github.com/gulpjs/gulp"
)

// recorder collects the events of an instance as "start:name", "stop:name" and "error:name:message".
type recorder struct {
	mu     sync.Mutex
	events []string
}

func record(g *gulp.Gulp) *recorder {
	r := &recorder{}
	add := func(s string) {
		r.mu.Lock()
		r.events = append(r.events, s)
		r.mu.Unlock()
	}
	g.On("start", func(e *gulp.Event) { add("start:" + e.Name) })
	g.On("stop", func(e *gulp.Event) { add("stop:" + e.Name) })
	g.On("error", func(e *gulp.Event) { add("error:" + e.Name + ":" + e.Error.Error()) })
	return r
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// run calls task and waits for its completion.
func run(t *testing.T, task *gulp.Task) error {
	t.Helper()
	ch := make(chan error, 1)
	task.Fn(func(err error) { ch <- err })
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("task did not complete")
	}
	return nil
}

func sync0(name string) *gulp.Task {
	return &gulp.Task{DisplayName: name, Fn: func(done gulp.Done) { done(nil) }}
}

func after(name string, d time.Duration, err error) *gulp.Task {
	return &gulp.Task{DisplayName: name, Fn: func(done gulp.Done) {
		time.AfterFunc(d, func() { done(err) })
	}}
}

func mustCompose(t *testing.T, fn func(...any) (*gulp.Task, error), args ...any) *gulp.Task {
	t.Helper()
	task, err := fn(args...)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestOrdering(t *testing.T) {
	t.Run("parallel starts every child before any runs", func(t *testing.T) {
		g := gulp.New()
		r := record(g)
		p := mustCompose(t, g.Parallel, sync0("a"), after("b", 20*time.Millisecond, nil), sync0("c"))
		if err := run(t, p); err != nil {
			t.Fatal(err)
		}
		want := []string{"start:a", "start:b", "start:c", "stop:a", "stop:c", "stop:b"}
		if got := r.list(); !reflect.DeepEqual(got, want) {
			t.Fatalf("events %v, want %v", got, want)
		}
	})

	t.Run("nested series and parallel", func(t *testing.T) {
		g := gulp.New()
		r := record(g)
		inner := mustCompose(t, g.Parallel, sync0("b"), sync0("c"))
		s := mustCompose(t, g.Series, sync0("a"), inner, sync0("d"))
		if err := run(t, s); err != nil {
			t.Fatal(err)
		}
		want := []string{"start:a", "stop:a", "start:<parallel>", "start:b", "start:c", "stop:b", "stop:c",
			"stop:<parallel>", "start:d", "stop:d"}
		if got := r.list(); !reflect.DeepEqual(got, want) {
			t.Fatalf("events %v, want %v", got, want)
		}
	})

	t.Run("series stops at the first error", func(t *testing.T) {
		g := gulp.New()
		r := record(g)
		boom := errors.New("boom")
		failing := &gulp.Task{DisplayName: "x", Fn: func(done gulp.Done) { done(boom) }}
		s := mustCompose(t, g.Series, sync0("a"), failing, sync0("b"))
		if err := run(t, s); err != boom {
			t.Fatalf("error %v, want boom", err)
		}
		want := []string{"start:a", "stop:a", "start:x", "error:x:boom"}
		if got := r.list(); !reflect.DeepEqual(got, want) {
			t.Fatalf("events %v, want %v", got, want)
		}
	})

	t.Run("settle mode runs everything and collects errors", func(t *testing.T) {
		g := gulp.New()
		g.SetSettle(true)
		e1, e2 := errors.New("one"), errors.New("two")
		s := mustCompose(t, g.Series,
			&gulp.Task{DisplayName: "x", Fn: func(done gulp.Done) { done(e1) }},
			sync0("a"),
			&gulp.Task{DisplayName: "y", Fn: func(done gulp.Done) { done(e2) }})
		err := run(t, s)
		var se *gulp.SettleError
		if !errors.As(err, &se) || len(se.Errors) != 2 || se.Errors[0] != e1 || se.Errors[1] != e2 {
			t.Fatalf("error %#v, want a SettleError with both errors", err)
		}
		if got, want := se.Error(), "Error: one,Error: two"; got != want {
			t.Fatalf("SettleError text %q, want %q", got, want)
		}
	})

	t.Run("a panic fails the task", func(t *testing.T) {
		g := gulp.New()
		s := mustCompose(t, g.Series, &gulp.Task{Fn: func(gulp.Done) { panic("thrown") }})
		if err := run(t, s); err == nil || err.Error() != "thrown" {
			t.Fatalf("error %v, want thrown", err)
		}
	})

	t.Run("done twice counts once", func(t *testing.T) {
		g := gulp.New()
		twice := &gulp.Task{Fn: func(done gulp.Done) {
			done(nil)
			done(errors.New("ignored"))
		}}
		if err := run(t, mustCompose(t, g.Series, twice)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("done from another goroutine", func(t *testing.T) {
		g := gulp.New()
		task := &gulp.Task{Fn: func(done gulp.Done) { go done(nil) }}
		if err := run(t, mustCompose(t, g.Parallel, task, task)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTaskRegistration(t *testing.T) {
	g := gulp.New()
	if err := g.Task("", &gulp.Task{Fn: func(gulp.Done) {}}); err == nil || err.Error() != "Task name must be specified" {
		t.Fatalf("empty name: %v", err)
	}
	var ae *gulp.AssertionError
	if err := g.Task("x", nil); !errors.As(err, &ae) || ae.Message != "Task function must be specified" {
		t.Fatalf("nil task: %v", err)
	}
	named := &gulp.Task{DisplayName: "named", Description: "desc", Fn: func(done gulp.Done) { done(nil) }}
	if err := g.Task("", named); err != nil {
		t.Fatal(err)
	}
	got := g.GetTask("named")
	if got == nil || got == named || got.Unwrap() != named || got.DisplayName != "named" {
		t.Fatalf("GetTask returned %#v", got)
	}
	if g.GetTask("missing") != nil {
		t.Fatal("unknown task found")
	}
	for _, name := range []string{"b", "10", "a", "2", "01"} {
		if err := g.Task(name, named); err != nil {
			t.Fatal(err)
		}
	}
	var labels []string
	for _, n := range g.Tree(false).Nodes {
		labels = append(labels, n.Label)
	}
	if want := []string{"2", "10", "named", "b", "a", "01"}; !reflect.DeepEqual(labels, want) {
		t.Fatalf("tree labels %v, want %v (JavaScript key order)", labels, want)
	}
}

func TestComposeErrors(t *testing.T) {
	g := gulp.New()
	for _, name := range []string{"build", "built", "test"} {
		if err := g.Task(name, sync0(name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Series(); err == nil || err.Error() != "One or more tasks should be combined using series or parallel" {
		t.Fatalf("empty series: %v", err)
	}
	_, err := g.Parallel("buil")
	var missing *gulp.TaskNotDefinedError
	if !errors.As(err, &missing) || err.Error() != "Task never defined: buil - did you mean? build, built" {
		t.Fatalf("typo: %v", err)
	}
	if _, err := g.Parallel("biuld"); err == nil || err.Error() != "Task never defined: biuld - did you mean? build" {
		t.Fatalf("transposition: %v", err)
	}
	if _, err := g.Parallel("zzzzzz"); err == nil || err.Error() != "Task never defined: zzzzzz" {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := g.Parallel(7.0); err == nil || err.Error() != "Task never defined: 7" {
		t.Fatalf("numeric: %v", err)
	}
}

func TestTree(t *testing.T) {
	g := gulp.New()
	a := sync0("a")
	if err := g.Task("a", a); err != nil {
		t.Fatal(err)
	}
	inner := mustCompose(t, g.Parallel, "a", a)
	if err := g.Task("default", mustCompose(t, g.Series, inner)); err != nil {
		t.Fatal(err)
	}
	tree := g.Tree(true)
	def := tree.Nodes[1]
	if def.Label != "default" || def.Type != "task" || len(def.Nodes) != 1 {
		t.Fatalf("default node %+v", def)
	}
	series := def.Nodes[0]
	if series.Label != "<series>" || !series.Branch || series.Nodes[0].Label != "<parallel>" {
		t.Fatalf("series node %+v", series)
	}
	kids := series.Nodes[0].Nodes
	if kids[0].Type != "task" || kids[1].Type != "function" {
		t.Fatalf("a by name is a task node, by function a function node: %+v %+v", kids[0], kids[1])
	}
}

func TestLastRun(t *testing.T) {
	g := gulp.New()
	ok := sync0("ok")
	if err := g.Task("ok", ok); err != nil {
		t.Fatal(err)
	}
	if err := g.Task("alias", ok); err != nil {
		t.Fatal(err)
	}
	if _, defined, err := g.LastRun("ok", 0); err != nil || defined {
		t.Fatalf("before running: %v %v", defined, err)
	}
	before := time.Now().UnixMilli()
	if err := run(t, mustCompose(t, g.Series, "ok")); err != nil {
		t.Fatal(err)
	}
	ts, defined, err := g.LastRun("alias", 0)
	if err != nil || !defined || ts < before || ts > time.Now().UnixMilli() {
		t.Fatalf("after running via another name: %d %v %v", ts, defined, err)
	}
	rounded, _, err := g.LastRun(ok, 1000)
	if err != nil || rounded != ts-ts%1000 {
		t.Fatalf("resolution 1000: %d from %d (%v)", rounded, ts, err)
	}
	if _, _, err := g.LastRun("nope", 0); err == nil || err.Error() != "Only functions can check lastRun" {
		t.Fatalf("unknown name: %v", err)
	}
	failing := &gulp.Task{Fn: func(done gulp.Done) { done(errors.New("x")) }}
	if err := g.Task("failing", failing); err != nil {
		t.Fatal(err)
	}
	if err := run(t, mustCompose(t, g.Series, "failing")); err == nil {
		t.Fatal("failing task succeeded")
	}
	if _, defined, _ := g.LastRun("failing", 0); defined {
		t.Fatal("a failed run is recorded")
	}
}

// customRegistry records Init calls and prefixes nothing; it stores tasks like the default registry.
type customRegistry struct {
	gulp.Registry
	inits int
}

func (r *customRegistry) Init(g *gulp.Gulp) { r.inits++ }

func TestSetRegistry(t *testing.T) {
	g := gulp.New()
	if err := g.Task("a", sync0("a")); err != nil {
		t.Fatal(err)
	}
	r := &customRegistry{Registry: gulp.NewDefaultRegistry()}
	if err := g.SetRegistry(r); err != nil {
		t.Fatal(err)
	}
	if r.inits != 1 || g.Registry() != r || r.Get("a") == nil {
		t.Fatalf("registry not adopted: inits=%d", r.inits)
	}
	if err := g.SetRegistry(nil); err == nil || !strings.Contains(err.Error(), "Custom registry must have `get` function") {
		t.Fatalf("nil registry: %v", err)
	}
}
