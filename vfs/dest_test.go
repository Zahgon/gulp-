package vfs

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// This file ports gulp 5.0.1's test/dest.js; the subtests carry the original it() descriptions. Every subtest
// starts and ends with an empty testdata/out-fixtures, like the original's beforeEach and afterEach.

// destCase pipes src(glob, srcOpts) into dest(outpath) and returns the files dest emitted.
func destCase(t *testing.T, glob string, srcOpts Options, outpath string) []string {
	t.Helper()
	in, err := Src(glob, srcOpts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Dest(outpath, nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := in.Pipe(out).Wait()
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path()
	}
	return paths
}

func TestDest(t *testing.T) {
	dir := testDir(t)
	outpath := filepath.Join(dir, "out-fixtures")
	clean := func(t *testing.T) {
		t.Helper()
		if err := os.RemoveAll(outpath); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, fn func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			clean(t)
			t.Cleanup(func() { clean(t) })
			fn(t)
		})
	}

	run("should return a stream", func(t *testing.T) {
		s, err := Dest(filepath.Join(dir, "./fixtures/"), nil)
		if err != nil || s == nil {
			t.Fatalf("Dest = %v, %v; want a stream", s, err)
		}
	})

	run("should return a output stream that writes files", func(t *testing.T) {
		in, err := Src("./fixtures/**/*.txt", Options{"cwd": dir})
		if err != nil {
			t.Fatal(err)
		}
		out, err := Dest(outpath, nil)
		if err != nil {
			t.Fatal(err)
		}
		files, err := in.Pipe(out).Wait()
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("got %d files, want 1", len(files))
		}
		if want := filepath.Join(outpath, "./copy/example.txt"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
		if string(files[0].Contents()) != "this is a test" {
			t.Errorf("re-emitted contents = %q", files[0].Contents())
		}
		data, err := os.ReadFile(filepath.Join(outpath, "copy", "example.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "this is a test" {
			t.Errorf("written contents = %q", data)
		}
	})

	run("should return a output stream that does not write non-read files", func(t *testing.T) {
		in, err := Src("./fixtures/**/*.txt", Options{"read": false, "cwd": dir})
		if err != nil {
			t.Fatal(err)
		}
		out, err := Dest(outpath, nil)
		if err != nil {
			t.Fatal(err)
		}
		files, err := in.Pipe(out).Wait()
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || !files[0].IsNull() {
			t.Fatalf("got %d files (null contents expected)", len(files))
		}
		if want := filepath.Join(outpath, "./copy/example.txt"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
		if _, err := os.ReadFile(filepath.Join(outpath, "copy", "example.txt")); err == nil {
			t.Error("a non-read file was written")
		}
	})

	run("should return a output stream that writes streaming files", func(t *testing.T) {
		in, err := Src("./fixtures/**/*.txt", Options{"buffer": false, "cwd": dir})
		if err != nil {
			t.Fatal(err)
		}
		out, err := Dest(outpath, nil)
		if err != nil {
			t.Fatal(err)
		}
		files, err := in.Pipe(out).Wait()
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || !files[0].IsStream() {
			t.Fatalf("got %d files (streaming contents expected)", len(files))
		}
		if want := filepath.Join(outpath, "./copy/example.txt"); files[0].Path() != want {
			t.Errorf("path = %q, want %q", files[0].Path(), want)
		}
		reread, err := io.ReadAll(files[0].ContentsStream())
		if err != nil {
			t.Fatal(err)
		}
		if string(reread) != "this is a test" {
			t.Errorf("re-emitted stream = %q", reread)
		}
		data, err := os.ReadFile(filepath.Join(outpath, "copy", "example.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "this is a test" {
			t.Errorf("written contents = %q", data)
		}
	})

	testWriteDir := func(srcOpts Options) func(t *testing.T) {
		return func(t *testing.T) {
			paths := destCase(t, "./fixtures/stuff", srcOpts, outpath)
			if len(paths) != 1 || paths[0] != filepath.Join(outpath, "./stuff") {
				t.Fatalf("emitted %q, want [%q]", paths, filepath.Join(outpath, "./stuff"))
			}
			if _, err := os.Stat(filepath.Join(outpath, "stuff")); err != nil {
				t.Errorf("stuff was not created: %v", err)
			}
		}
	}

	run("should return a output stream that writes streaming files into new directories",
		testWriteDir(Options{"cwd": dir}))
	run("should return a output stream that writes streaming files into new directories (buffer: false)",
		testWriteDir(Options{"buffer": false, "cwd": dir}))
	run("should return a output stream that writes streaming files into new directories (read: false)",
		testWriteDir(Options{"read": false, "cwd": dir}))
	run("should return a output stream that writes streaming files into new directories (read: false, buffer: false)",
		testWriteDir(Options{"buffer": false, "read": false, "cwd": dir}))
}
