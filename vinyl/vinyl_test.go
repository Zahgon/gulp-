package vinyl

import (
	"io"
	"strings"
	"testing"
)

func TestPathSettersNormalise(t *testing.T) {
	f := New(Options{Cwd: "/cwd/", Path: "/cwd/a//b/../c.txt/"})
	if got := f.Path(); got != "/cwd/a/c.txt" {
		t.Errorf("Path() = %q, want /cwd/a/c.txt", got)
	}
	if got := f.Cwd(); got != "/cwd" {
		t.Errorf("Cwd() = %q, want /cwd", got)
	}
	// Short strings keep their trailing separator, as remove-trailing-separator does.
	short := New(Options{Cwd: "/", Path: "é/"})
	if got := short.Path(); got != "é/" {
		t.Errorf("Path() = %q, want é/", got)
	}
}

func TestHistory(t *testing.T) {
	f := New(Options{Cwd: "/", History: []string{"/a.txt"}, Path: "/b.txt"})
	if err := f.SetPath("/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetPath(""); err != nil {
		t.Fatal(err)
	}
	if err := f.SetPath("/c.txt"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/a.txt", "/b.txt", "/c.txt"}
	got := f.History()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("History() = %q, want %q", got, want)
	}
	if New(Options{}).History() == nil {
		t.Error("History() of a file without path is nil, want an empty slice")
	}
}

func TestDerivedProperties(t *testing.T) {
	f := New(Options{Cwd: "/root", Base: "/root/src", Path: "/root/src/lib/app.min.js"})
	checks := []struct {
		name string
		get  func() (string, error)
		want string
	}{
		{"relative", f.Relative, "lib/app.min.js"},
		{"dirname", f.Dirname, "/root/src/lib"},
		{"basename", f.Basename, "app.min.js"},
		{"stem", f.Stem, "app.min"},
		{"extname", f.Extname, ".js"},
	}
	for _, c := range checks {
		got, err := c.get()
		if err != nil || got != c.want {
			t.Errorf("%s = %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	steps := []struct {
		set  func() error
		want string
	}{
		{func() error { return f.SetExtname(".ts") }, "/root/src/lib/app.min.ts"},
		{func() error { return f.SetStem("main") }, "/root/src/lib/main.ts"},
		{func() error { return f.SetBasename("index.d.ts") }, "/root/src/lib/index.d.ts"},
		{func() error { return f.SetDirname("/out") }, "/out/index.d.ts"},
	}
	for i, s := range steps {
		if err := s.set(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := f.Path(); got != s.want {
			t.Errorf("step %d: Path() = %q, want %q", i, got, s.want)
		}
	}
}

func TestErrorsWithoutPath(t *testing.T) {
	f := New(Options{Cwd: "/"})
	if _, err := f.Relative(); err == nil || err.Error() != "No path specified! Can not get relative." {
		t.Errorf("Relative() error = %v", err)
	}
	if err := f.SetStem("x"); err == nil || err.Error() != "No path specified! Can not set stem." {
		t.Errorf("SetStem() error = %v", err)
	}
	if err := f.SetRelative("x"); err == nil ||
		err.Error() != "File.relative is generated from the base and path attributes. Do not modify it." {
		t.Errorf("SetRelative() error = %v", err)
	}
	if err := f.SetCwd(""); err == nil || err.Error() != "cwd must be a non-empty string." {
		t.Errorf("SetCwd(\"\") error = %v", err)
	}
}

func TestBaseFollowsCwd(t *testing.T) {
	f := New(Options{Cwd: "/a", Base: "/a/"})
	if got := f.Base(); got != "/a" {
		t.Errorf("Base() = %q, want /a", got)
	}
	if err := f.SetCwd("/b"); err != nil {
		t.Fatal(err)
	}
	if got := f.Base(); got != "/b" {
		t.Errorf("Base() after SetCwd = %q, want /b (base equal to cwd is not stored)", got)
	}
	if err := f.SetBase("/c"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetBase(""); err != nil {
		t.Fatal(err)
	}
	if got := f.Base(); got != "/b" {
		t.Errorf("Base() after reset = %q, want /b", got)
	}
}

func TestContentKinds(t *testing.T) {
	dir := &Stat{Mode: 0o040755}
	link := &Stat{Mode: 0o120777}
	cases := []struct {
		name                                      string
		f                                         *File
		isBuffer, isStream, isNull, isDir, isLink bool
	}{
		{"buffer", New(Options{Contents: []byte{}}), true, false, false, false, false},
		{"stream", New(Options{Stream: strings.NewReader("x")}), false, true, false, false, false},
		{"null", New(Options{}), false, false, true, false, false},
		{"directory", New(Options{Stat: dir}), false, false, true, true, false},
		{"directory with contents", New(Options{Stat: dir, Contents: []byte("x")}), true, false, false, false, false},
		{"symbolic", New(Options{Stat: link}), false, false, true, false, true},
	}
	for _, c := range cases {
		f := c.f
		got := []bool{f.IsBuffer(), f.IsStream(), f.IsNull(), f.IsDirectory(), f.IsSymbolic()}
		want := []bool{c.isBuffer, c.isStream, c.isNull, c.isDir, c.isLink}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: predicates = %v, want %v", c.name, got, want)
				break
			}
		}
	}
}

func TestString(t *testing.T) {
	long := make([]byte, 52)
	cases := []struct {
		f    *File
		want string
	}{
		{New(Options{Cwd: "/a", Path: "/a/b/c.txt", Contents: []byte("hi")}), `<File "b/c.txt" <Buffer 68 69>>`},
		{New(Options{Contents: []byte{}}), `<File <Buffer >>`},
		{New(Options{Cwd: "/"}), `<File >`},
		{New(Options{Cwd: "/a", Path: "/a"}), `<File >`},
		{New(Options{Cwd: "/", Path: "/x", Stream: strings.NewReader("")}), `<File "x" <ReadableStream>>`},
		{New(Options{Cwd: "/", Path: "/x", Contents: long[:51]}),
			`<File "x" <Buffer ` + strings.TrimSpace(strings.Repeat("00 ", 50)) + ` ... 1 more byte>>`},
		{New(Options{Cwd: "/", Path: "/x", Contents: long}),
			`<File "x" <Buffer ` + strings.TrimSpace(strings.Repeat("00 ", 50)) + ` ... 2 more bytes>>`},
	}
	for _, c := range cases {
		if got := c.f.String(); got != c.want {
			t.Errorf("String() = %s, want %s", got, c.want)
		}
	}
}

func TestClone(t *testing.T) {
	f := New(Options{Cwd: "/", Path: "/a.txt", Contents: []byte("abc"), Stat: &Stat{Mode: 0o100644}})
	f.SetSourceMapKeys(map[string]any{"version": 3.0, "sources": []any{"a"}}, []string{"version", "sources"})
	c := f.Clone()
	c.Contents()[0] = 'X'
	c.Stat.Mode = 0
	c.SourceMap()["sources"].([]any)[0] = "changed"
	if string(f.Contents()) != "abc" || f.Stat.Mode != 0o100644 || f.SourceMap()["sources"].([]any)[0] != "a" {
		t.Error("Clone shares state with the original")
	}
	if got := strings.Join(c.SourceMapKeys(), ","); got != "version,sources" {
		t.Errorf("SourceMapKeys() of clone = %q", got)
	}

	s := New(Options{Cwd: "/", Path: "/s.txt", Stream: strings.NewReader("streamed")})
	sc := s.Clone()
	for name, file := range map[string]*File{"original": s, "clone": sc} {
		data, err := io.ReadAll(file.ContentsStream())
		if err != nil || string(data) != "streamed" {
			t.Errorf("%s stream = %q, %v", name, data, err)
		}
	}
}

func TestSymlinkAndIsVinyl(t *testing.T) {
	f := New(Options{})
	f.SetSymlink("a//b/")
	if got := f.Symlink(); got != "a/b" {
		t.Errorf("Symlink() = %q, want a/b", got)
	}
	if !IsVinyl(f) || IsVinyl((*File)(nil)) || IsVinyl("file") {
		t.Error("IsVinyl misreports")
	}
}

func TestSourceMapKeyOrder(t *testing.T) {
	f := New(Options{})
	f.SetSourceMapKeys(map[string]any{"b": 1, "a": 2}, []string{"b", "a"})
	f.SetSourceMap(map[string]any{"b": 1, "a": 2, "c": 3})
	if got := strings.Join(f.SourceMapKeys(), ","); got != "b,a,c" {
		t.Errorf("SourceMapKeys() = %q, want b,a,c", got)
	}
	f.SetSourceMap(nil)
	if f.SourceMap() != nil || f.SourceMapKeys() != nil {
		t.Error("SetSourceMap(nil) kept the map")
	}
}
