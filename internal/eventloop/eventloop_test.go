package eventloop

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// collect runs setup on the loop and returns the order in which the labelled callbacks ran, once want entries
// were recorded.
func collect(t *testing.T, want int, setup func(log func(string))) []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	done := make(chan struct{})
	log := func(s string) {
		mu.Lock()
		got = append(got, s)
		if len(got) == want {
			close(done)
		}
		mu.Unlock()
	}
	Post(func() { setup(log) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	return got
}

func TestTicksRunBeforeMacrotasks(t *testing.T) {
	got := collect(t, 5, func(log func(string)) {
		Post(func() { log("macro") })
		NextTick(func() {
			log("tick1")
			NextTick(func() { log("tick3") })
		})
		NextTick(func() { log("tick2") })
		Dispatch(func() { log("sync") })
	})
	want := []string{"sync", "tick1", "tick2", "tick3", "macro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestThrowDefersRemainingTicks(t *testing.T) {
	got := collect(t, 4, func(log func(string)) {
		Post(func() { log("macro") })
		NextTick(func() {
			log("throws")
			Throw()
		})
		NextTick(func() { log("after") })
		NextTick(func() { log("last") })
	})
	want := []string{"throws", "macro", "after", "last"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestTimersAndIdle(t *testing.T) {
	got := collect(t, 3, func(log func(string)) {
		stopped := AfterFunc(10*time.Millisecond, func() { log("stopped timer ran") })
		AfterFunc(30*time.Millisecond, func() { log("late") })
		AfterFunc(20*time.Millisecond, func() { log("early") })
		stopped.Stop()
		WhenIdle(func() { log("idle") })
	})
	want := []string{"early", "late", "idle"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestOnLoop(t *testing.T) {
	if OnLoop() {
		t.Fatal("test goroutine reported as the loop")
	}
	got := collect(t, 1, func(log func(string)) {
		if OnLoop() {
			log("on loop")
		} else {
			log("off loop")
		}
	})
	if got[0] != "on loop" {
		t.Fatal(got[0])
	}
}

func TestHaltDropsWork(t *testing.T) {
	ran := make(chan string, 2)
	Halt()
	Post(func() { ran <- "dropped" })
	Resume()
	Post(func() { ran <- "kept" })
	select {
	case s := <-ran:
		if s != "kept" {
			t.Fatalf("got %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}
