package glob

import (
	"strings"
	"testing"
)

// Expected values below were recorded from the pinned JavaScript originals (picomatch 2.3.2, anymatch 3.1.3,
// is-glob 4.0.3, glob-parent 6.0.2, @gulpjs/to-absolute-glob 4.0.0, normalize-path 3.0.0).

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		opts          Options
		want          bool
	}{
		{"*.js", "a.js", Options{}, true},
		{"*.js", "a/b.js", Options{}, false},
		{"*.js", ".a.js", Options{}, false},
		{"*.js", ".a.js", Options{Dot: true}, true},
		{"**/*.js", "a/b/c.js", Options{}, true},
		{"**/*.js", "a/.b/c.js", Options{}, false},
		{"**", "a/.b", Options{Dot: true}, true},
		{"a/{b,c}/d", "a/c/d", Options{}, true},
		{"a/{b,c}/d", "a/{b,c}/d", Options{NoBrace: true}, true},
		{"a{1..3}", "a2", Options{}, true},
		{"a{1..3}", "a4", Options{}, false},
		{"{a..c}", "b", Options{}, true},
		{"+(a|b)", "abab", Options{}, true},
		{"+(a|b)", "+(a|b)", Options{NoExt: true}, true},
		{"!(a)", "b", Options{}, true},
		{"!(a)", "a", Options{}, false},
		{"*.!(js)", "a.ts", Options{}, true},
		{"*.!(js)", "a.js", Options{}, false},
		{"[[:alpha:]]", "x", Options{}, true},
		{"[[:alpha:]]", "1", Options{}, false},
		{"[!a]", "b", Options{}, false},
		{"[^a]", "a", Options{}, false},
		{"?", "é", Options{}, true},
		{"?", "😀", Options{}, false},
		{"*.JS", "a.js", Options{NoCase: true}, true},
		{"*.js", "dir/a.js", Options{MatchBase: true}, true},
		{"!*.js", "a.md", Options{}, true},
		{"a/**", "a", Options{}, true},
		{"a/**/b", "a/b", Options{}, true},
		{"a/**/b", "a/x/y/b", Options{}, true},
		{"a/**/b", "a/x/y/b", Options{NoGlobstar: true}, false},
		{"a(b", "a(b", Options{}, true},
		{"a$b", "a$b", Options{}, true},
		{"a|b", "b", Options{}, false},
		{"a+", "a+", Options{}, true},
		{`\\*`, "*", Options{}, false},
		{`\\*`, "a", Options{}, false},
		{"*", "a", Options{Ignore: []string{"a"}}, false},
		{"@(a|b)c", "bc", Options{}, true},
		{"a*(z)", "azzz", Options{}, true},
		{"!(foo).!(bar)", "foo.bar", Options{}, false},
		{"!(!(foo)).!(!(bar))", "foo.bar", Options{}, true},
		{"+(a|aa)", "aaa", Options{}, false},
		{"+(a|aa)", "+(a|aa)", Options{}, true},
		{"[z-a]", "a", Options{}, false},
	}
	for _, c := range cases {
		got, err := Match(c.pattern, c.path, c.opts)
		if err != nil {
			t.Errorf("Match(%q, %q, %+v) error %v", c.pattern, c.path, c.opts, err)
			continue
		}
		if got != c.want {
			t.Errorf("Match(%q, %q, %+v) = %v, want %v", c.pattern, c.path, c.opts, got, c.want)
		}
	}
}

func TestMatchErrors(t *testing.T) {
	if _, err := Match("", "a", Options{}); err == nil || err.Error() != "Expected pattern to be a non-empty string" {
		t.Errorf("empty pattern error = %v", err)
	}
	long := strings.Repeat("x", 65537)
	want := "Input length: 65537, exceeds maximum allowed length: 65536"
	if _, err := Match(long, "a", Options{}); err == nil || err.Error() != want {
		t.Errorf("long pattern error = %v", err)
	}
	_, err := NewMatcher([]string{""}, Options{})
	if err == nil || err.Error() != "Expected pattern to be a non-empty string" {
		t.Errorf("anymatch empty glob error = %v", err)
	}
	// picomatch 2.3.2 loops forever on a trailing run of four or more backslashes outside its fast path.
	if _, err := Match("("+strings.Repeat(`\`, 4), "a", Options{}); err != errNoTermination {
		t.Errorf("trailing backslash run error = %v", err)
	}
	// picomatch drops the opening token of a risky extglob that a brace range already removed and throws.
	_, err = Match("{a..+(b}|)", "ab", Options{})
	if err == nil || err.Error() != "Cannot set properties of undefined (setting 'type')" {
		t.Errorf("removed extglob token error = %v", err)
	}
	if _, err := NewMatcher([]string{"a"}, Options{Ignore: []string{""}}); err == nil {
		t.Error("empty ignore pattern should fail")
	}
}

func TestMatcherIndex(t *testing.T) {
	m, err := NewMatcher([]string{"!*.min.js", "*.js", "*.md"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"a.js": 1, "a.min.js": -1, "a.md": 2, "x": -1, "a\\b.md": -1} {
		if got := m.Index(path); got != want {
			t.Errorf("Index(%q) = %d, want %d", path, got, want)
		}
	}
	exact, err := NewMatcher([]string{"a[b"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := exact.Index("a[b"); got != 0 {
		t.Errorf("exact string match index = %d, want 0", got)
	}
}

func TestIsGlobAndParent(t *testing.T) {
	cases := []struct {
		s      string
		glob   bool
		parent string
	}{
		{"a/*.js", true, "a"},
		{"a/{b,c}/d", true, "a"},
		{"!(a)", true, "."},
		{"a/b", false, "a"},
		{`\\*.js`, true, "."},
		{"a/[b]/c", true, "a"},
		{"(a|b)", true, "."},
		{"a?b", false, "."},
		{"a/b/", false, "a/b"},
		{`a\\{b}`, true, "."},
	}
	for _, c := range cases {
		if got := IsGlob(c.s); got != c.glob {
			t.Errorf("IsGlob(%q) = %v, want %v", c.s, got, c.glob)
		}
		if got := Parent(c.s); got != c.parent {
			t.Errorf("Parent(%q) = %q, want %q", c.s, got, c.parent)
		}
	}
	if IsGlob("") {
		t.Error("IsGlob(\"\") = true")
	}
}

func TestIsNegated(t *testing.T) {
	for _, c := range []struct {
		in, pattern string
		negated     bool
	}{
		{"!a", "a", true},
		{"!(a)", "!(a)", false},
		{"a", "a", false},
		{"!!a", "!a", true},
		{"", "", false},
	} {
		p, n := IsNegated(c.in)
		if p != c.pattern || n != c.negated {
			t.Errorf("IsNegated(%q) = %q, %v", c.in, p, n)
		}
	}
}

func TestToAbsolute(t *testing.T) {
	for _, c := range []struct{ glob, cwd, root, want string }{
		{"*.js", "/c/d", "", "/c/d/*.js"},
		{"./a", "/c/d", "", "/c/d/a"},
		{"!./a", "/c/d", "", "!/c/d/a"},
		{"/x/*", "/c/d", "/r", "/r/x/*"},
		{"../a", "/c/d", "", "/c/a"},
		{"a/", "/c(1)", "", `/c\(1\)/a/`},
	} {
		if got := ToAbsolute(c.glob, c.cwd, c.root, "/wd"); got != c.want {
			t.Errorf("ToAbsolute(%q, %q, %q) = %q, want %q", c.glob, c.cwd, c.root, got, c.want)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	for _, c := range []struct {
		in    string
		strip bool
		want  string
	}{
		{`a\b//c/`, true, "a/b/c"},
		{`a\b//c/`, false, "a/b/c/"},
		{"/", true, "/"},
		{`\\?\c:\x`, true, "//?/c:/x"},
		{`\\\\?\\c:\\x`, true, "/?/c:/x"},
		{"a", true, "a"},
	} {
		if got := NormalizePath(c.in, c.strip); got != c.want {
			t.Errorf("NormalizePath(%q, %v) = %q, want %q", c.in, c.strip, got, c.want)
		}
	}
}

func TestJSRegexpSyntax(t *testing.T) {
	for src, valid := range map[string]bool{
		"a{1}":       true,
		"{1}":        false,
		"a**":        false,
		"(?<=a)*":    false,
		"(?=a)*":     true,
		"[z-a]":      false,
		"[a-\\d]":    true,
		"a{2,1}":     false,
		"(a":         false,
		"a)":         false,
		"(?x)":       false,
		"\\":         false,
		"(?<n>a)\\k": false,
		"\\k":        true,
		"{":          true,
		"a{,2}":      true,
	} {
		_, err := compileJSRegexp([]rune(src), false)
		if (err == nil) != valid {
			t.Errorf("compileJSRegexp(%q) error = %v, want valid %v", src, err, valid)
		}
	}
}

// FuzzMatch checks that arbitrary patterns, paths and option combinations never panic. Paths stay short because
// some patterns backtrack exponentially, in picomatch exactly as here.
func FuzzMatch(f *testing.F) {
	for _, s := range []string{"*.js", "**/a/**", "!(a|b)", "+(a|aa)", "{a..c}", "[[:alpha:]-z]", `\\(?<n>a)\k<n>`,
		"{a..+(b}|)", "a/**/b", "*(a|*(b))"} {
		f.Add(s, "a/b.js", uint8(0))
	}
	f.Fuzz(func(t *testing.T, pattern, path string, flags uint8) {
		if len(pattern) > 32 || len(path) > 12 {
			return
		}
		opts := Options{Dot: flags&1 != 0, NoBrace: flags&2 != 0, NoGlobstar: flags&4 != 0, NoExt: flags&8 != 0,
			NoCase: flags&16 != 0, MatchBase: flags&32 != 0}
		_, _ = Match(pattern, path, opts)
		IsGlob(pattern)
		Parent(pattern)
		ToAbsolute(pattern, "/c", "", "/wd")
		NormalizePath(path, flags&64 != 0)
	})
}
