package gulp_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gulp "github.com/gulpjs/gulp"
	"github.com/gulpjs/gulp/cli"
)

// TestGulp ports test/index.test.js.
func TestGulp(t *testing.T) {
	t.Run("hasOwnProperty", func(t *testing.T) {
		g := gulp.New()
		typ := reflect.TypeOf(g)
		for _, name := range []string{"src", "dest", "symlink", "watch", "task", "series", "parallel", "tree", "lastRun", "registry"} {
			t.Run(name, func(t *testing.T) {
				method := strings.ToUpper(name[:1]) + name[1:]
				if _, ok := typ.MethodByName(method); !ok {
					t.Fatalf("Gulp has no %s method", method)
				}
			})
		}
	})

	t.Run("can run against gulpfile.cjs", func(t *testing.T) {
		g := gulp.New()
		if err := g.Task("default", &gulp.Task{Fn: func(done gulp.Done) { done(nil) }}); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runCLI(t, g, "gulpfile.cjs", nil)
		if code != 0 {
			t.Fatalf("exit code %d, stderr %q", code, stderr)
		}
		if !strings.Contains(stdout, "gulpfile.cjs") {
			t.Fatalf("stdout %q does not name the gulpfile", stdout)
		}
		if stderr != "" {
			t.Fatalf("stderr %q", stderr)
		}
	})

	t.Run("can run against gulpfile.mjs", func(t *testing.T) {
		g := gulp.New()
		var missing []string
		task := &gulp.Task{Fn: func(done gulp.Done) {
			// the ESM fixture asserts the destructured API is made of functions
			for _, fn := range []any{g.Watch, g.Task, g.Series, g.Parallel, g.Registry, g.Tree, g.LastRun, g.Src, g.Dest, g.Symlink, g.On} {
				if reflect.ValueOf(fn).IsNil() {
					missing = append(missing, reflect.TypeOf(fn).String())
				}
			}
			done(nil)
		}}
		if err := g.Task("default", task); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runCLI(t, g, "gulpfile.mjs", nil)
		if code != 0 || len(missing) > 0 {
			t.Fatalf("exit code %d, missing %v, stderr %q", code, missing, stderr)
		}
		if !strings.Contains(stdout, "gulpfile.mjs") {
			t.Fatalf("stdout %q does not name the gulpfile", stdout)
		}
		if stderr != "" {
			t.Fatalf("stderr %q", stderr)
		}
	})
}

// runCLI runs cli.Run with a gulpfile of the given name in a temporary folder and returns the exit code and
// the captured output. The working directory is restored afterwards (Run changes into the gulpfile's folder).
func runCLI(t *testing.T, g *gulp.Gulp, name string, args []string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	gulpfile := filepath.Join(dir, name)
	if err := os.WriteFile(gulpfile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Error(err)
		}
	})
	return capture(t, func() int { return cli.Run(g, gulpfile, args) })
}

// capture runs fn with os.Stdout and os.Stderr redirected to pipes.
func capture(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	var outBuf, errBuf bytes.Buffer
	done := make(chan struct{}, 2)
	go func() {
		if _, err := io.Copy(&outBuf, outR); err != nil {
			t.Error(err)
		}
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(&errBuf, errR); err != nil {
			t.Error(err)
		}
		done <- struct{}{}
	}()
	code := fn()
	os.Stdout, os.Stderr = stdout, stderr
	if err := outW.Close(); err != nil {
		t.Error(err)
	}
	if err := errW.Close(); err != nil {
		t.Error(err)
	}
	<-done
	<-done
	return code, outBuf.String(), errBuf.String()
}
