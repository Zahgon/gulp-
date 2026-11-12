package gulp_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	gulp "github.com/gulpjs/gulp"
	"github.com/gulpjs/gulp/vfs"
)

const tempFileContent = "A test generated this file and it is safe to delete"

func createTempFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(tempFileContent), 0o644); err != nil {
		t.Fatal(err)
	}
}

// updateTempFile appends to path after 125 ms, as the original test helper does.
func updateTempFile(t *testing.T, path string) {
	t.Helper()
	time.AfterFunc(125*time.Millisecond, func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := f.WriteString(" changed"); err != nil {
			t.Error(err)
		}
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
}

// removeTempFile removes path after 125 ms.
func removeTempFile(t *testing.T, path string) {
	t.Helper()
	time.AfterFunc(125*time.Millisecond, func() {
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
	})
}

// wait fails the test unless ch receives within five seconds.
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the watch task")
	}
}

func closeWatcher(t *testing.T, w *atomic.Pointer[gulp.Watcher]) {
	if err := w.Load().Close(); err != nil {
		t.Error(err)
	}
}

// TestWatch ports test/watch.js.
func TestWatch(t *testing.T) {
	outpath := func(t *testing.T) string {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("should call the function when file changes: no options", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "watch-func.txt")
		createTempFile(t, tempFile)
		g := gulp.New()
		called := make(chan struct{}, 1)
		var watcher atomic.Pointer[gulp.Watcher]
		w, err := g.Watch("watch-func.txt", vfs.Options{"cwd": out}, gulp.TaskFunc(func(cb gulp.Done) {
			closeWatcher(t, &watcher)
			cb(nil)
			called <- struct{}{}
		}))
		if err != nil {
			t.Fatal(err)
		}
		watcher.Store(w)
		updateTempFile(t, tempFile)
		wait(t, called)
	})

	t.Run("should execute the gulp.parallel tasks", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "watch-func.txt")
		createTempFile(t, tempFile)
		g := gulp.New()
		called := make(chan struct{}, 1)
		var watcher atomic.Pointer[gulp.Watcher]
		err := g.Task("test", &gulp.Task{Fn: func(cb gulp.Done) {
			closeWatcher(t, &watcher)
			cb(nil)
			called <- struct{}{}
		}})
		if err != nil {
			t.Fatal(err)
		}
		p, err := g.Parallel("test")
		if err != nil {
			t.Fatal(err)
		}
		w, err := g.Watch("watch-func.txt", vfs.Options{"cwd": out}, p)
		if err != nil {
			t.Fatal(err)
		}
		watcher.Store(w)
		updateTempFile(t, tempFile)
		wait(t, called)
	})

	t.Run("should work with destructuring", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "watch-func.txt")
		g := gulp.New()
		watch, parallel, task := g.Watch, g.Parallel, g.Task
		createTempFile(t, tempFile)
		called := make(chan struct{}, 1)
		var watcher atomic.Pointer[gulp.Watcher]
		err := task("test", &gulp.Task{Fn: func(cb gulp.Done) {
			closeWatcher(t, &watcher)
			cb(nil)
			called <- struct{}{}
		}})
		if err != nil {
			t.Fatal(err)
		}
		p, err := parallel("test")
		if err != nil {
			t.Fatal(err)
		}
		w, err := watch("watch-func.txt", vfs.Options{"cwd": out}, p)
		if err != nil {
			t.Fatal(err)
		}
		watcher.Store(w)
		updateTempFile(t, tempFile)
		wait(t, called)
	})

	t.Run("should not call the function when no file changes: no options", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "watch-func.txt")
		createTempFile(t, tempFile)
		time.Sleep(250 * time.Millisecond)
		g := gulp.New()
		var calls atomic.Int32
		w, err := g.Watch("watch-func.txt", vfs.Options{"cwd": out}, gulp.TaskFunc(func(cb gulp.Done) {
			calls.Add(1)
			cb(nil)
		}))
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if n := calls.Load(); n != 0 {
			t.Fatalf("watch task ran %d times", n)
		}
	})

	t.Run("should call the function when file changes: w/ options", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "watch-func-options.txt")
		createTempFile(t, tempFile)
		g := gulp.New()
		called := make(chan struct{}, 1)
		var watcher atomic.Pointer[gulp.Watcher]
		w, err := g.Watch("watch-func-options.txt", vfs.Options{"cwd": out}, gulp.TaskFunc(func(cb gulp.Done) {
			closeWatcher(t, &watcher)
			cb(nil)
			called <- struct{}{}
		}))
		if err != nil {
			t.Fatal(err)
		}
		watcher.Store(w)
		updateTempFile(t, tempFile)
		wait(t, called)
	})

	t.Run("should call the function when file changes at a path with japanese characters", func(t *testing.T) {
		out := outpath(t)
		japaneseDir := filepath.Join(out, "フォルダ")
		if err := os.Mkdir(japaneseDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tempFile := filepath.Join(japaneseDir, "foobar.txt")
		createTempFile(t, tempFile)
		g := gulp.New()
		called := make(chan struct{}, 1)
		var watcher atomic.Pointer[gulp.Watcher]
		w, err := g.Watch("フォルダ/*", vfs.Options{"cwd": out}, gulp.TaskFunc(func(cb gulp.Done) {
			closeWatcher(t, &watcher)
			cb(nil)
			called <- struct{}{}
		}))
		if err != nil {
			t.Fatal(err)
		}
		watcher.Store(w)
		updateTempFile(t, tempFile)
		wait(t, called)
	})

	t.Run("should not call the function when ignored file changes", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "ignored.txt")
		createTempFile(t, tempFile)
		g := gulp.New()
		var calls atomic.Int32
		w, err := g.Watch([]string{"*", "!ignored.txt"}, vfs.Options{"cwd": out}, gulp.TaskFunc(func(cb gulp.Done) {
			calls.Add(1)
			cb(nil)
		}))
		if err != nil {
			t.Fatal(err)
		}
		removeTempFile(t, tempFile)
		time.Sleep(time.Second)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if n := calls.Load(); n != 0 {
			t.Fatalf("watch task ran %d times", n)
		}
	})

	t.Run("should not drop options when no callback specified", func(t *testing.T) {
		out := outpath(t)
		tempFile := filepath.Join(out, "watch-func-nodrop-options.txt")
		relFile := "../watch-func-nodrop-options.txt"
		cwd := filepath.Join(out, "subdir")
		createTempFile(t, tempFile)
		g := gulp.New()
		w, err := g.Watch(relFile, vfs.Options{"cwd": cwd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		changed := make(chan string, 1)
		w.On("change", func(filepath string) {
			select {
			case changed <- filepath:
			default:
			}
		})
		updateTempFile(t, tempFile)
		select {
		case p := <-changed:
			if p == "" {
				t.Fatal("change event without a path")
			}
			if got := filepath.Join(cwd, p); got != tempFile {
				t.Fatalf("change path resolves to %q, want %q", got, tempFile)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no change event")
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("should work without options or callback", func(t *testing.T) {
		w, err := gulp.New().Watch("x", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	})

	manyTasks := func(t *testing.T, glob func(out string) string, opts func(out string) vfs.Options, file string) {
		out := outpath(t)
		tempFile := filepath.Join(out, file)
		createTempFile(t, tempFile)
		g := gulp.New()
		a := 0
		called := make(chan struct{}, 1)
		var watcher atomic.Pointer[gulp.Watcher]
		if err := g.Task("task1", &gulp.Task{Fn: func(cb gulp.Done) {
			a++
			cb(nil)
		}}); err != nil {
			t.Fatal(err)
		}
		if err := g.Task("task2", &gulp.Task{Fn: func(cb gulp.Done) {
			a += 10
			if a != 11 {
				t.Errorf("a = %d, want 11", a)
			}
			closeWatcher(t, &watcher)
			cb(nil)
			called <- struct{}{}
		}}); err != nil {
			t.Fatal(err)
		}
		s, err := g.Series("task1", "task2")
		if err != nil {
			t.Fatal(err)
		}
		w, err := g.Watch(glob(out), opts(out), s)
		if err != nil {
			t.Fatal(err)
		}
		watcher.Store(w)
		updateTempFile(t, tempFile)
		wait(t, called)
	}

	t.Run("should run many tasks: w/ options", func(t *testing.T) {
		manyTasks(t, func(string) string { return "watch-task-options.txt" },
			func(out string) vfs.Options { return vfs.Options{"cwd": out} }, "watch-task-options.txt")
	})

	t.Run("should run many tasks: no options", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		// relative paths are resolved by the file system from the physical working directory
		if wd, err = filepath.EvalSymlinks(wd); err != nil {
			t.Fatal(err)
		}
		manyTasks(t, func(out string) string {
			rel, err := filepath.Rel(wd, filepath.Join(out, "watch-many-tasks-no-options.txt"))
			if err != nil {
				t.Fatal(err)
			}
			return "./" + rel
		}, func(string) vfs.Options { return nil }, "watch-many-tasks-no-options.txt")
	})

	t.Run("should throw an error: passed parameter (string) is not a function", func(t *testing.T) {
		out := outpath(t)
		filename := "empty.txt"
		createTempFile(t, filepath.Join(out, filename))
		_, err := gulp.New().Watch(filename, vfs.Options{"cwd": out}, "task1")
		want := "watching " + filename + ": watch task has to be a function (optionally generated by using gulp.parallel or gulp.series)"
		if err == nil || err.Error() != want {
			t.Fatalf("error %v, want %q", err, want)
		}
	})

	t.Run("should throw an error: passed parameter (array) is not a function", func(t *testing.T) {
		out := outpath(t)
		filename := "empty.txt"
		createTempFile(t, filepath.Join(out, filename))
		_, err := gulp.New().Watch(filename, vfs.Options{"cwd": out}, []string{"task1"})
		want := "watching " + filename + ": watch task has to be a function (optionally generated by using gulp.parallel or gulp.series)"
		if err == nil || err.Error() != want {
			t.Fatalf("error %v, want %q", err, want)
		}
	})
}

// TestAtomicRecreate removes a watched file and writes it again one scan later: with atomic on (the default) the
// unlink and the add are one change, however late the scans that see them run.
func TestAtomicRecreate(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a.txt")
	createTempFile(t, file)
	w, err := gulp.New().Watch("a.txt", vfs.Options{"cwd": dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	}()
	events := make(chan string, 8)
	for _, name := range []string{"add", "change", "unlink"} {
		name := name
		w.On(name, func(p string) { events <- name + " " + p })
	}
	time.Sleep(200 * time.Millisecond)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	createTempFile(t, file)
	select {
	case e := <-events:
		if e != "change a.txt" {
			t.Fatalf("got %q, want %q", e, "change a.txt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the change")
	}
	select {
	case e := <-events:
		t.Fatalf("unexpected second event %q", e)
	case <-time.After(500 * time.Millisecond):
	}
}
