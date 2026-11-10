package vfs

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gulpjs/gulp/vinyl"
)

// Unit tests that need no Node.js; expected values were taken from the original packages.

func TestStreamComposition(t *testing.T) {
	a := vinyl.New(vinyl.Options{Cwd: "/", Path: "/a"})
	b := vinyl.New(vinyl.Options{Cwd: "/", Path: "/b"})
	dropB := Through(func(f *vinyl.File) (*vinyl.File, error) {
		if f.Path() == "/b" {
			return nil, nil
		}
		return f, nil
	})
	s := FromFiles(a, nil, b).Pipe(dropB)
	files, err := s.Wait()
	if err != nil || len(files) != 1 || files[0] != a {
		t.Fatalf("Wait() = %v, %v; want [a]", files, err)
	}
	if again, err := s.Wait(); err != nil || len(again) != 0 {
		t.Errorf("second Wait() = %v, %v; want nothing", again, err)
	}

	boom := errors.New("boom")
	failing := FromFiles(a, b).Pipe(Through(func(*vinyl.File) (*vinyl.File, error) { return nil, boom }))
	if _, err := failing.Wait(); !errors.Is(err, boom) {
		t.Errorf("Wait() error = %v, want boom", err)
	}
	var nilStream *Stream
	if _, err := nilStream.Wait(); err == nil {
		t.Error("Wait() on a nil stream succeeded")
	}
}

func TestSrcArgumentErrors(t *testing.T) {
	cases := []struct {
		globs any
		opts  Options
		want  string
	}{
		{"", nil, "Invalid glob argument: "},
		{[]string{}, nil, "Invalid glob argument: "},
		{[]any{"a", 5}, nil, "Invalid glob argument: a,5"},
		{nil, nil, "Invalid glob argument: undefined"},
		{42, nil, "Invalid glob argument: 42"},
		{[]string{"!a"}, nil, "Missing positive glob"},
		{"a", Options{"cwd": 5}, "The `cwd` option must be a string"},
		{"a", Options{"cwd": nil}, "The `cwd` option must be a string"},
		{"a", Options{"dot": "yes"}, "The `dot` option must be a boolean"},
		{"a", Options{"uniqueBy": 3}, "The `uniqueBy` option must be a string or function"},
		{"a", Options{"base": 5}, "The `base` option must be a string if specified"},
		{"a", Options{"ignore": 5}, "The `ignore` option must be a string or array"},
	}
	for _, c := range cases {
		if _, err := Src(c.globs, c.opts); err == nil || err.Error() != c.want {
			t.Errorf("Src(%v, %v) error = %v, want %q", c.globs, c.opts, err, c.want)
		}
	}
}

func TestSrcSingularGlob(t *testing.T) {
	dir := t.TempDir()
	s, err := Src("missing.txt", Options{"cwd": dir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Wait()
	want := "File not found with singular glob: " + filepath.ToSlash(dir) +
		"/missing.txt (if this was purposeful, use `allowEmpty` option)"
	if err == nil || !strings.HasSuffix(err.Error(), "/missing.txt (if this was purposeful, use `allowEmpty` option)") {
		t.Errorf("error = %v, want %q", err, want)
	}
	s, err = Src("missing.txt", Options{"cwd": dir, "allowEmpty": true})
	if err != nil {
		t.Fatal(err)
	}
	if files, err := s.Wait(); err != nil || len(files) != 0 {
		t.Errorf("allowEmpty: %v, %v", files, err)
	}
}

func TestDestArgumentErrors(t *testing.T) {
	for _, folder := range []any{"", nil, 0, false} {
		if _, err := Dest(folder, nil); err == nil ||
			err.Error() != "Invalid dest() folder argument. Please specify a non-empty string or a function." {
			t.Errorf("Dest(%v) error = %v", folder, err)
		}
		if _, err := Symlink(folder, nil); err == nil ||
			err.Error() != "Invalid symlink() folder argument. Please specify a non-empty string or a function." {
			t.Errorf("Symlink(%v) error = %v", folder, err)
		}
	}
	f := vinyl.New(vinyl.Options{Cwd: "/", Path: "/x.txt", Contents: []byte("x")})
	d, err := Dest(func(*vinyl.File) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromFiles(f).Pipe(d).Wait(); err == nil || err.Error() != "Invalid output folder" {
		t.Errorf("empty folder function: %v", err)
	}
}

func TestSrcDestRoundTrip(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	mtime := time.UnixMilli(1500000000123)
	src := filepath.Join(in, "a.js")
	if err := os.WriteFile(src, []byte("\xef\xbb\xbfvar a;\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	s, err := Src("*.js", Options{"cwd": in, "sourcemaps": true})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Dest(out, Options{"sourcemaps": ".", "encoding": "utf16le"})
	if err != nil {
		t.Fatal(err)
	}
	files, err := s.Pipe(d).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("emitted %d files, want the file and its map", len(files))
	}
	written, err := os.ReadFile(filepath.Join(out, "a.js"))
	if err != nil {
		t.Fatal(err)
	}
	wantText := "var a;\n//# sourceMappingURL=a.js.map\n"
	if want := encodeUCS2(utf8ToUnits(wantText)); !bytes.Equal(written, want) {
		t.Errorf("written = %q, want UTF-16LE of %q", written, wantText)
	}
	mapData, err := os.ReadFile(filepath.Join(out, "a.js.map"))
	if err != nil {
		t.Fatal(err)
	}
	wantMap := `{"version":3,"names":[],"mappings":"","sources":["a.js"],"sourcesContent":["var a;\n"],"file":"a.js"}`
	// The encoding option applies to the map file as well.
	if !bytes.Equal(mapData, encodeUCS2(utf8ToUnits(wantMap))) {
		t.Errorf("map = %s\nwant %s", mapData, wantMap)
	}
	st, err := os.Stat(filepath.Join(out, "a.js"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 || !st.ModTime().Equal(mtime) {
		t.Errorf("mode %v mtime %v, want 0640 and %v", st.Mode().Perm(), st.ModTime(), mtime)
	}
	if files[0].Stat.Mode&0o7777 != 0o640 {
		t.Errorf("emitted stat mode %o", files[0].Stat.Mode)
	}
}

func TestSymlinkStage(t *testing.T) {
	in := t.TempDir()
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(in, "t.txt"), []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Src("t.txt", Options{"cwd": in})
	if err != nil {
		t.Fatal(err)
	}
	l, err := Symlink(out, Options{"relativeSymlinks": true})
	if err != nil {
		t.Fatal(err)
	}
	files, err := s.Pipe(l).Wait()
	if err != nil || len(files) != 1 {
		t.Fatalf("Wait() = %v, %v", files, err)
	}
	target, err := os.Readlink(filepath.Join(out, "t.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if target != files[0].Symlink() || !files[0].IsSymbolic() {
		t.Errorf("link target %q, file symlink %q, symbolic %v", target, files[0].Symlink(), files[0].IsSymbolic())
	}
}

func TestCodecs(t *testing.T) {
	decodeCases := []struct {
		enc, in   string
		removeBOM bool
		want      string
	}{
		{"utf8", "efbbbf41", true, "41"},
		{"utf8", "efbbbf41", false, "efbbbf41"},
		{"utf8", "41e28241eda080f09f", true, "41efbfbd41efbfbdefbfbdefbfbdefbfbd"},
		{"latin1", "e9", false, "c3a9"},
		{"utf16", "fffe4100", false, "41"},
		{"utf16", "00410042", false, "4142"},
		{"cesu8", "41e282", true, "4130efbfbd"},
		{"hex", "0aff", false, "30616666"},
		{"utf32be", "0001f600", false, "f09f9880"},
		{"cp437", "80", false, "c387"},
	}
	for _, c := range decodeCases {
		in, err := hex.DecodeString(c.in)
		if err != nil {
			t.Fatal(err)
		}
		cd := getCodec(c.enc)
		if cd == nil {
			t.Fatalf("no codec %q", c.enc)
		}
		got := hex.EncodeToString(decodeContents(in, c.enc, cd, cd.bomAware && c.removeBOM))
		if got != c.want {
			t.Errorf("decode %s %s = %s, want %s", c.enc, c.in, got, c.want)
		}
	}
	if getCodec("bogus") != nil || getCodec("shift_jis") != nil {
		t.Error("unexpected codec for an unsupported encoding")
	}
	if getCodec("ISO_8859-1:1987") == nil || getCodec("UTF-16LE") == nil {
		t.Error("canonical encoding names are not resolved")
	}
	if _, err := getCodec("base64").encodeBuffer(utf8ToUnits("QUJDRA")); err != errConcatList {
		t.Errorf("base64 tail error = %v", err)
	}
	if got := getCodec("utf16").encodeStream(nil); len(got) != 0 {
		t.Errorf("stream encoding of empty text = %x, want nothing", got)
	}
}

func TestJSONStringify(t *testing.T) {
	m := map[string]any{"b": "< \x01\"", "10": 1.5, "2": []any{nil, 1e21, 1e-7, 123456789.0}, "a": map[string]any{"y": true, "x": false}}
	got := stringifyObject(m, []string{"b", "10", "2", "a"})
	want := `{"2":[null,1e+21,1e-7,123456789],"10":1.5,"b":"<` + " " + `\u0001\"","a":{"x":false,"y":true}}`
	if got != want {
		t.Errorf("stringify = %s\nwant %s", got, want)
	}
	for n, want := range map[float64]string{0.1: "0.1", -5e-7: "-5e-7", 1e300: "1e+300", 123e18: "123000000000000000000", math.Inf(1): "Infinity"} {
		if got := formatJSNumber(n); got != want {
			t.Errorf("formatJSNumber(%v) = %s, want %s", n, got, want)
		}
	}
	if _, _, err := parseJSONObject(`{"a":1} x`); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestSourceMapComments(t *testing.T) {
	inline := "a\n  //# sourceMappingURL=data:application/json;charset=utf-8;base64,e30=\nb"
	c, ok := lastInlineComment(inline)
	if !ok || c != "  //# sourceMappingURL=data:application/json;charset=utf-8;base64,e30=" {
		t.Errorf("lastInlineComment = %q, %v", c, ok)
	}
	if got := removeInlineComments(inline); got != "a\n\nb" {
		t.Errorf("removeInlineComments = %q", got)
	}
	for src, want := range map[string]string{
		"x //# sourceMappingURL=a.map  \n": "a.map",
		"/*# sourceMappingURL=b c.map */":  "b c.map",
		`//# sourceMappingURL="q.map"`:     "",
	} {
		got, _ := firstMapFileComment(src)
		if got != want {
			t.Errorf("firstMapFileComment(%q) = %q, want %q", src, got, want)
		}
	}
	f := vinyl.New(vinyl.Options{Cwd: "/", Path: "/s.css", Stream: strings.NewReader("a{}\r\n")})
	f.SetSourceMap(map[string]any{"version": 3.0})
	if _, err := writeSourceMap(f, "", false); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f.ContentsStream())
	if err != nil {
		t.Fatal(err)
	}
	if want := "a{}\r\n/*# sourceMappingURL=data:application/json;charset=utf-8;base64,eyJ2ZXJzaW9uIjozfQ== */\r\n"; string(data) != want {
		t.Errorf("appended stream = %q, want %q", data, want)
	}
}

func TestResolveOptions(t *testing.T) {
	f := vinyl.New(vinyl.Options{Cwd: "/", Path: "/a.js"})
	r := newResolver(srcConfig, Options{
		"buffer":   "no",
		"read":     func(f *vinyl.File) bool { return false },
		"encoding": func(*vinyl.File) any { return 5 },
		"since":    1.9,
	})
	if !r.boolean("buffer", f) {
		t.Error("a string buffer option did not fall back to true")
	}
	if r.boolean("read", f) {
		t.Error("a read function was not called")
	}
	if got := r.resolve("encoding", f); got != "utf8" {
		t.Errorf("encoding = %v, want the utf8 default", got)
	}
	if got := r.resolve("since", f); got != 1.0 {
		t.Errorf("since = %v, want 1 (TimeClip)", got)
	}
	d := newResolver(destConfig, nil)
	if got := d.resolve("mode", vinyl.New(vinyl.Options{Stat: &vinyl.Stat{Mode: 0o100600}})); got != float64(0o100600) {
		t.Errorf("mode default = %v", got)
	}
}
