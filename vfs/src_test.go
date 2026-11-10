package vfs

import (
	"bytes"
	"io"
	"path/filepath"
	"testing"

	"github.com/gulpjs/gulp/vinyl"
)

// This file ports gulp 5.0.1's test/src.js; the subtests carry the original it() descriptions.

// testDir is the absolute testdata folder, the __dirname of the original tests.
func testDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// srcFiles runs Src and drains it.
func srcFiles(t *testing.T, globs any, opts Options) []*vinyl.File {
	t.Helper()
	s, err := Src(globs, opts)
	if err != nil {
		t.Fatalf("Src(%v): %v", globs, err)
	}
	files, err := s.Wait()
	if err != nil {
		t.Fatalf("Src(%v) stream: %v", globs, err)
	}
	return files
}

func TestSrc(t *testing.T) {
	dir := testDir(t)

	t.Run("should return a stream", func(t *testing.T) {
		s, err := Src("./fixtures/*.coffee", Options{"cwd": dir})
		if err != nil || s == nil {
			t.Fatalf("Src = %v, %v; want a stream", s, err)
		}
	})

	t.Run("should return a input stream from a flat glob", func(t *testing.T) {
		files := srcFiles(t, "./fixtures/*.coffee", Options{"cwd": dir})
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		f := files[0]
		if want := filepath.Join(dir, "fixtures/test.coffee"); f.Path() != want {
			t.Errorf("path = %q, want %q", f.Path(), want)
		}
		if !bytes.Equal(f.Contents(), []byte("this is a test")) {
			t.Errorf("contents = %q", f.Contents())
		}
	})

	t.Run("should return a input stream for multiple globs", func(t *testing.T) {
		globs := []string{"./fixtures/stuff/run.dmc", "./fixtures/stuff/test.dmc"}
		files := srcFiles(t, globs, Options{"cwd": dir})
		if len(files) != 2 {
			t.Fatalf("got %d files, want 2", len(files))
		}
		for i, g := range globs {
			if want := filepath.Join(dir, g); files[i].Path() != want {
				t.Errorf("files[%d].path = %q, want %q", i, files[i].Path(), want)
			}
		}
	})

	t.Run("should return a input stream for multiple globs, with negation", func(t *testing.T) {
		globs := []string{"./fixtures/stuff/*.dmc", "!fixtures/stuff/test.dmc"}
		files := srcFiles(t, globs, Options{"cwd": dir})
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		if want := filepath.Join(dir, "fixtures/stuff/run.dmc"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
	})

	t.Run("should return a input stream with no contents when read is false", func(t *testing.T) {
		files := srcFiles(t, "./fixtures/*.coffee", Options{"read": false, "cwd": dir})
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		if !files[0].IsNull() {
			t.Error("contents are not null")
		}
		if want := filepath.Join(dir, "fixtures/test.coffee"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
	})

	t.Run("should return a input stream with contents as stream when buffer is false", func(t *testing.T) {
		files := srcFiles(t, "./fixtures/*.coffee", Options{"buffer": false, "cwd": dir})
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		f := files[0]
		if !f.IsStream() {
			t.Fatal("contents are not a stream")
		}
		data, err := io.ReadAll(f.ContentsStream())
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "this is a test" {
			t.Errorf("streamed contents = %q", data)
		}
		if want := filepath.Join(dir, "fixtures/test.coffee"); f.Path() != want {
			t.Errorf("path = %q, want %q", f.Path(), want)
		}
	})

	t.Run("should return a input stream from a deep glob", func(t *testing.T) {
		files := srcFiles(t, "./fixtures/**/*.jade", Options{"cwd": dir})
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		if want := filepath.Join(dir, "fixtures/test/run.jade"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
		if string(files[0].Contents()) != "test template" {
			t.Errorf("contents = %q", files[0].Contents())
		}
	})

	t.Run("should return a input stream from a deeper glob", func(t *testing.T) {
		files := srcFiles(t, "./fixtures/**/*.dmc", Options{"cwd": dir})
		if len(files) != 2 {
			t.Errorf("got %d files, want 2", len(files))
		}
	})

	t.Run("should return a file stream from a flat path", func(t *testing.T) {
		files := srcFiles(t, filepath.Join(dir, "./fixtures/test.coffee"), nil)
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		if want := filepath.Join(dir, "fixtures/test.coffee"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
		if string(files[0].Contents()) != "this is a test" {
			t.Errorf("contents = %q", files[0].Contents())
		}
	})
}
