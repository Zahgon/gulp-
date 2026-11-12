package gulp

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/gulpjs/gulp/internal/eventloop"
	"github.com/gulpjs/gulp/vfs"
)

func TestWatchOptionDefaults(t *testing.T) {
	c, err := watchOptions(vfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.persistent || !c.followSymlinks || c.disableGlobbing || c.usePolling || !c.atomic || c.hasDepth {
		t.Fatalf("defaults: %+v", c)
	}
	if c.interval != 100*time.Millisecond || c.binaryInterval != 300*time.Millisecond {
		t.Fatalf("polling defaults: %v %v", c.interval, c.binaryInterval)
	}
	polling, err := watchOptions(vfs.Options{"usePolling": true})
	if err != nil {
		t.Fatal(err)
	}
	if polling.atomic {
		t.Fatal("usePolling must turn the default atomic off")
	}
	awf, err := watchOptions(vfs.Options{"awaitWriteFinish": map[string]any{"stabilityThreshold": 300.0}})
	if err != nil {
		t.Fatal(err)
	}
	if !awf.awaitWriteFinish || awf.stabilityThreshold != 300*time.Millisecond || awf.awfPollInterval != 100*time.Millisecond {
		t.Fatalf("awaitWriteFinish: %+v", awf)
	}
}

func TestReadablePermissionDigits(t *testing.T) {
	dir := t.TempDir()
	w := &Watcher{}
	for mode, want := range map[os.FileMode]bool{0o644: true, 0o200: false, 0o044: true, 0o000: false, 0o004: true} {
		p := filepath.Join(dir, mode.String())
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := w.readable(info); got != want {
			t.Errorf("mode %o: readable %v, want %v", mode, got, want)
		}
	}
}

func TestIgnoreRules(t *testing.T) {
	w := &Watcher{cwd: "/base"}
	rules, err := w.ignoreRules([]any{"*.log", func(p string) bool { return p == "/base/x" }, regexp.MustCompile(`tmp$`), 5})
	if err != nil {
		t.Fatal(err)
	}
	ignored := func(p string) bool {
		for _, r := range rules {
			if r.match(p, nil) {
				return true
			}
		}
		return false
	}
	for p, want := range map[string]bool{"/base/a.log": true, "/base/x": true, "/base/y.tmp": true, "/base/a.txt": false} {
		if got := ignored(p); got != want {
			t.Errorf("%s: ignored %v, want %v", p, got, want)
		}
	}
}

func TestWatcherAddAndUnwatch(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Created and listened to on the loop, as a task does, so the listener is in place before the initial adds.
	events := make(chan string, 16)
	var w *Watcher
	created := make(chan struct{})
	eventloop.Post(func() {
		defer close(created)
		w, err = New().Watch("a.txt", vfs.Options{"cwd": dir, "ignoreInitial": false}, nil)
		if err == nil {
			w.OnAll(func(event, path string) { events <- event + " " + path })
		}
	})
	<-created
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no event, want %q", want)
		}
	}
	expect("add a.txt")
	if err := w.Add("b.txt"); err != nil {
		t.Fatal(err)
	}
	expect("add b.txt")
	if err := w.Unwatch("a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect("change b.txt")
	if err := w.Add([]any{1}); err == nil || err.Error() != "Non-string provided as watch path: 1" {
		t.Fatalf("Add with a number: %v", err)
	}
}

func TestSettleNewFilesAcrossSkippedScans(t *testing.T) {
	s := entryState{size: 1, ino: 7}
	fresh := map[string]entryState{}
	always := func(string) bool { return true }
	never := func(string) bool { return false }
	cur := map[string]entryState{"/w/a.txt": s}
	settleNewFiles(map[string]entryState{}, cur, fresh, always)
	if len(cur) != 0 || len(fresh) != 1 {
		t.Fatalf("first sight: cur %v, fresh %v", cur, fresh)
	}
	// a polling scan before the file's interval came leaves it out of cur; it stays held
	settleNewFiles(map[string]entryState{}, map[string]entryState{}, fresh, never)
	if len(fresh) != 1 {
		t.Fatalf("skipped scan dropped the held file: %v", fresh)
	}
	cur = map[string]entryState{"/w/a.txt": s}
	settleNewFiles(map[string]entryState{}, cur, fresh, always)
	if _, ok := cur["/w/a.txt"]; !ok || len(fresh) != 0 {
		t.Fatalf("second sight: cur %v, fresh %v", cur, fresh)
	}
	// a due scan that no longer finds a held file forgets it
	settleNewFiles(map[string]entryState{}, map[string]entryState{"/w/b.txt": s}, fresh, always)
	settleNewFiles(map[string]entryState{}, map[string]entryState{}, fresh, always)
	if len(fresh) != 0 {
		t.Fatalf("gone file still held: %v", fresh)
	}
}
