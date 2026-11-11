// Package glob reproduces the glob helpers glob-stream 8.0.4 relies on: picomatch 2.3.2 matching as anymatch 3.1.3
// drives it, is-glob 4.0.3, is-negated-glob 1.0.0, normalize-path 3.0.0, glob-parent 6.0.2 and
// @gulpjs/to-absolute-glob 4.0.0. Behaviour follows the posix (Linux) branches of those packages.
package glob

import (
	"errors"
	"strings"
)

// Options are the picomatch options glob-stream forwards from the gulp src option bag.
type Options struct {
	Dot        bool     // picomatch dot
	NoBrace    bool     // nobrace
	NoGlobstar bool     // noglobstar
	NoExt      bool     // noext (noextglob)
	NoCase     bool     // nocase
	MatchBase  bool     // matchBase / basename
	Ignore     []string // picomatch ignore option (already absolute globs)
}

// errEmptyPattern is the TypeError picomatch throws for an empty pattern.
var errEmptyPattern = errors.New("Expected pattern to be a non-empty string")

func (o Options) pm() pmOptions {
	return pmOptions{dot: o.Dot, nobrace: o.NoBrace, noglobstar: o.NoGlobstar, noextglob: o.NoExt}
}

// picoMatcher is one compiled picomatch(pattern, options) function.
type picoMatcher struct {
	glob      string
	re        *jsRegexp
	matchBase bool
	ignore    []*picoMatcher
}

// compileOne follows picomatch(glob, options) for a single string pattern, ignore list included.
func compileOne(glob string, opts Options, withIgnore bool) (*picoMatcher, error) {
	if glob == "" {
		return nil, errEmptyPattern
	}
	src, err := makeRe(toUnits(glob), opts.pm())
	if err != nil {
		return nil, err
	}
	re, rerr := compileJSRegexp(src, opts.NoCase)
	if rerr != nil {
		re = neverRegexp()
	}
	m := &picoMatcher{glob: glob, re: re, matchBase: opts.MatchBase}
	if withIgnore && opts.Ignore != nil {
		for _, ig := range opts.Ignore {
			im, err := compileOne(ig, opts, false)
			if err != nil {
				return nil, err
			}
			m.ignore = append(m.ignore, im)
		}
	}
	return m, nil
}

// test follows picomatch.test for the posix platform.
func (m *picoMatcher) test(input string) bool {
	if input == "" {
		return false
	}
	if input == m.glob {
		return true
	}
	if m.matchBase {
		return m.re.test(toUnits(posixBasename(input)))
	}
	return m.re.test(toUnits(input))
}

// match mirrors the function picomatch returns.
func (m *picoMatcher) match(input string) bool {
	if !m.test(input) {
		return false
	}
	for _, ig := range m.ignore {
		if ig.match(input) {
			return false
		}
	}
	return true
}

// Matcher tests paths against a compiled list of globs: Index reports the first glob a path matches, and a
// negated glob in the list excludes the paths it matches.
type Matcher struct {
	negated  []*picoMatcher
	patterns []*picoMatcher
	indexes  []int
}

// NewMatcher compiles globs into one matcher with anymatch's rules: negated globs (leading "!") exclude, the rest
// include. It returns picomatch's error for an invalid pattern: "Expected pattern to be a non-empty string" for an
// empty one, the input length error for one over 65536 characters, and an error for the patterns picomatch 2.3.2
// never finishes parsing.
func NewMatcher(globs []string, opts Options) (*Matcher, error) {
	m := &Matcher{}
	for _, g := range globs {
		if strings.HasPrefix(g, "!") {
			pm, err := compileOne(g[1:], opts, true)
			if err != nil {
				return nil, err
			}
			m.negated = append(m.negated, pm)
		}
	}
	for i, g := range globs {
		if !strings.HasPrefix(g, "!") {
			pm, err := compileOne(g, opts, true)
			if err != nil {
				return nil, err
			}
			m.patterns = append(m.patterns, pm)
			m.indexes = append(m.indexes, i)
		}
	}
	return m, nil
}

// Index matches path and returns the index into the ORIGINAL globs slice of the first
// positive glob that matches, or -1 (also -1 when a negated glob matches first, exactly as anymatch decides).
func (m *Matcher) Index(path string) int {
	p := NormalizePath(path, false)
	for _, n := range m.negated {
		if n.match(p) {
			return -1
		}
	}
	for i, pm := range m.patterns {
		// anymatch's createPattern also accepts an exact string match before running picomatch.
		if pm.glob == p || pm.match(p) {
			return m.indexes[i]
		}
	}
	return -1
}

// Match reports whether str matches pattern with picomatch's rules (a leading "!" negates as picomatch does).
// Its errors are NewMatcher's.
func Match(pattern, str string, opts Options) (bool, error) {
	m, err := compileOne(pattern, opts, true)
	if err != nil {
		return false, err
	}
	return m.match(str), nil
}

// IsGlob reports whether s contains glob syntax, with the strict rules of is-glob 4.0.3.
func IsGlob(s string) bool {
	if s == "" {
		return false
	}
	u := toUnits(s)
	if isExtglob(u) {
		return true
	}
	return strictCheck(u)
}

// isExtglob is is-extglob 2.1.1.
func isExtglob(str []rune) bool {
	for len(str) > 0 {
		found := false
		for i := 0; i < len(str); i++ {
			if str[i] == '\\' && i+1 < len(str) && !isLineTerminator(str[i+1]) {
				str = str[i+2:]
				found = true
				break
			}
			if strings.ContainsRune("@?!+*", str[i]) && i+1 < len(str) && str[i+1] == '(' {
				for j := i + 2; j < len(str) && !isLineTerminator(str[j]); j++ {
					if str[j] == ')' {
						return true
					}
				}
			}
		}
		if !found {
			return false
		}
	}
	return false
}

func unitAt(s []rune, i int) rune {
	if i >= 0 && i < len(s) {
		return s[i]
	}
	return -1
}

func indexFrom(s []rune, c rune, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// strictCheck follows is-glob's default check.
func strictCheck(str []rune) bool {
	if str[0] == '!' {
		return true
	}
	index := 0
	pipeIndex, closeSquareIndex, closeCurlyIndex, closeParenIndex, backSlashIndex := -2, -2, -2, -2, -2
	for index < len(str) {
		c := str[index]
		if c == '*' {
			return true
		}
		if unitAt(str, index+1) == '?' && (c == ']' || c == '.' || c == '+' || c == ')') {
			return true
		}
		if closeSquareIndex != -1 && c == '[' && unitAt(str, index+1) != ']' {
			if closeSquareIndex < index {
				closeSquareIndex = indexFrom(str, ']', index)
			}
			if closeSquareIndex > index {
				if backSlashIndex == -1 || backSlashIndex > closeSquareIndex {
					return true
				}
				backSlashIndex = indexFrom(str, '\\', index)
				if backSlashIndex == -1 || backSlashIndex > closeSquareIndex {
					return true
				}
			}
		}
		if closeCurlyIndex != -1 && c == '{' && unitAt(str, index+1) != '}' {
			closeCurlyIndex = indexFrom(str, '}', index)
			if closeCurlyIndex > index {
				backSlashIndex = indexFrom(str, '\\', index)
				if backSlashIndex == -1 || backSlashIndex > closeCurlyIndex {
					return true
				}
			}
		}
		if closeParenIndex != -1 && c == '(' && unitAt(str, index+1) == '?' {
			if n2 := unitAt(str, index+2); (n2 == ':' || n2 == '!' || n2 == '=') && unitAt(str, index+3) != ')' {
				closeParenIndex = indexFrom(str, ')', index)
				if closeParenIndex > index {
					backSlashIndex = indexFrom(str, '\\', index)
					if backSlashIndex == -1 || backSlashIndex > closeParenIndex {
						return true
					}
				}
			}
		}
		if pipeIndex != -1 && c == '(' && unitAt(str, index+1) != '|' {
			if pipeIndex < index {
				pipeIndex = indexFrom(str, '|', index)
			}
			if pipeIndex != -1 && unitAt(str, pipeIndex+1) != ')' {
				closeParenIndex = indexFrom(str, ')', pipeIndex)
				if closeParenIndex > pipeIndex {
					backSlashIndex = indexFrom(str, '\\', pipeIndex)
					if backSlashIndex == -1 || backSlashIndex > closeParenIndex {
						return true
					}
				}
			}
		}
		if c == '\\' {
			open := unitAt(str, index+1)
			index += 2
			var closer rune = -1
			switch open {
			case '{':
				closer = '}'
			case '(':
				closer = ')'
			case '[':
				closer = ']'
			}
			if closer >= 0 {
				if n := indexFrom(str, closer, index); n != -1 {
					index = n + 1
				}
			}
			if unitAt(str, index) == '!' {
				return true
			}
		} else {
			index++
		}
	}
	return false
}

// Parent returns the folder part of a glob before its first glob segment, as glob-parent 6.0.2 computes it
// for glob-stream (posix, default options).
func Parent(s string) string {
	str := s
	if isEnclosure(str) {
		str += "/"
	}
	str += "a"
	for {
		str = posixDirname(str)
		if !isGlobby(str) {
			break
		}
	}
	return unescapeGlobParent(str)
}

func isEnclosure(str string) bool {
	if str == "" {
		return false
	}
	var start byte
	switch str[len(str)-1] {
	case '}':
		start = '{'
	case ']':
		start = '['
	default:
		return false
	}
	found := strings.IndexByte(str, start)
	if found < 0 {
		return false
	}
	inner := ""
	if found+1 <= len(str)-1 {
		inner = str[found+1 : len(str)-1]
	}
	return strings.Contains(inner, "/")
}

func isGlobby(str string) bool {
	// /\([^()]+$/
	if i := strings.LastIndexByte(str, '('); i >= 0 && i < len(str)-1 && !strings.ContainsAny(str[i+1:], "()") {
		return true
	}
	if str != "" && (str[0] == '{' || str[0] == '[') {
		return true
	}
	// /[^\\][{[]/
	for i := 1; i < len(str); i++ {
		if (str[i] == '{' || str[i] == '[') && str[i-1] != '\\' {
			return true
		}
	}
	return IsGlob(str)
}

// unescapeGlobParent is str.replace(/\\([!*?|[\](){}])/g, '$1').
func unescapeGlobParent(str string) string {
	var b strings.Builder
	for i := 0; i < len(str); i++ {
		if str[i] == '\\' && i+1 < len(str) && strings.IndexByte("!*?|[](){}", str[i+1]) >= 0 {
			b.WriteByte(str[i+1])
			i++
			continue
		}
		b.WriteByte(str[i])
	}
	return b.String()
}

// IsNegated reports whether s starts with a "!" that is not an extglob and returns the
// pattern without that "!".
func IsNegated(s string) (pattern string, negated bool) {
	if strings.HasPrefix(s, "!") && !strings.HasPrefix(s, "!(") {
		return s[1:], true
	}
	return s, false
}

// ToAbsolute makes glob absolute against cwd (or against root for a glob starting with "/"), keeping a leading
// "!" and a trailing slash, as @gulpjs/to-absolute-glob 4.0.0 does with options {cwd, root}; root "" means unset.
// cwd and wd (the process working directory, against which a relative cwd or root is resolved) are absolute.
func ToAbsolute(glob, cwd, root, wd string) string {
	cwd = escapeGlob(unixify(posixResolve(wd, unescapeGlob(cwd))))

	rootDir := root
	if rootDir != "" {
		rootDir = unixify(unescapeGlob(rootDir))
		if !strings.HasPrefix(rootDir, "/") {
			rootDir = unixify(posixResolve(wd, rootDir))
		}
		rootDir = escapeGlob(rootDir)
	}

	trailingSlash := strings.HasSuffix(glob, "/")
	pattern, negated := IsNegated(glob)
	glob = pattern
	if strings.HasPrefix(glob, "./") {
		glob = glob[2:]
	}
	if glob == "." {
		glob = ""
	}
	if rootDir != "" && strings.HasPrefix(glob, "/") {
		glob = joinGlob(rootDir, glob)
	} else if !strings.HasPrefix(glob, "/") || strings.HasPrefix(glob, "\\") {
		glob = joinGlob(cwd, glob)
	}
	if trailingSlash && !strings.HasSuffix(glob, "/") {
		glob += "/"
	}
	if negated {
		return "!" + glob
	}
	return glob
}

const globSpecials = "({[]})*?!"

func escapeGlob(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if strings.IndexByte(globSpecials, p[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func unescapeGlob(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+1 < len(p) && strings.IndexByte(globSpecials, p[i+1]) >= 0 {
			b.WriteByte(p[i+1])
			i++
			continue
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func unixify(p string) string { return strings.ReplaceAll(p, "\\", "/") }

func joinGlob(dir, glob string) string {
	if strings.HasSuffix(dir, "/") {
		dir = dir[:len(dir)-1]
	}
	if strings.HasPrefix(glob, "/") {
		glob = glob[1:]
	}
	if glob == "" {
		return dir
	}
	for strings.HasPrefix(glob, "../") {
		idx := strings.LastIndexByte(dir, '/')
		if idx >= 0 {
			dir = dir[:idx]
		} else if dir != "" {
			dir = dir[:len(dir)-1]
		}
		glob = glob[3:]
	}
	return dir + "/" + glob
}

// NormalizePath turns backslashes into slashes and collapses repeated slashes, removing a trailing slash when
// stripTrailing is set, as normalize-path 3.0.0 does.
func NormalizePath(p string, stripTrailing bool) string {
	if p == "\\" || p == "/" {
		return "/"
	}
	if len(toUnits(p)) <= 1 {
		return p
	}
	prefix := ""
	if len(p) > 4 && p[3] == '\\' {
		if ch := p[2]; (ch == '?' || ch == '.') && strings.HasPrefix(p, "\\\\") {
			p = p[2:]
			prefix = "//"
		}
	}
	var segs []string
	start := 0
	for i := 0; i < len(p); {
		if p[i] == '/' || p[i] == '\\' {
			segs = append(segs, p[start:i])
			for i < len(p) && (p[i] == '/' || p[i] == '\\') {
				i++
			}
			start = i
			continue
		}
		i++
	}
	segs = append(segs, p[start:])
	if stripTrailing && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	return prefix + strings.Join(segs, "/")
}

// posixResolve follows Node's path.posix.resolve for one argument, with wd as the working directory.
func posixResolve(wd, p string) string {
	resolved := ""
	absolute := false
	for _, seg := range []string{p, wd} {
		if seg == "" {
			continue
		}
		resolved = seg + "/" + resolved
		absolute = seg[0] == '/'
		if absolute {
			break
		}
	}
	resolved = normalizeString(resolved, !absolute)
	if absolute {
		return "/" + resolved
	}
	if resolved == "" {
		return "."
	}
	return resolved
}

// normalizeString follows Node's internal normalizeString for posix separators.
func normalizeString(path string, allowAboveRoot bool) string {
	res := ""
	lastSegmentLength := 0
	lastSlash := -1
	dots := 0
	var code byte
	for i := 0; i <= len(path); i++ {
		if i < len(path) {
			code = path[i]
		} else if code == '/' {
			break
		} else {
			code = '/'
		}
		if code == '/' {
			if lastSlash == i-1 || dots == 1 {
				// nothing to do
			} else if dots == 2 {
				if len(res) < 2 || lastSegmentLength != 2 || res[len(res)-1] != '.' || res[len(res)-2] != '.' {
					if len(res) > 2 {
						idx := strings.LastIndexByte(res, '/')
						if idx == -1 {
							res = ""
							lastSegmentLength = 0
						} else {
							res = res[:idx]
							lastSegmentLength = len(res) - 1 - strings.LastIndexByte(res, '/')
						}
						lastSlash = i
						dots = 0
						continue
					} else if len(res) != 0 {
						res = ""
						lastSegmentLength = 0
						lastSlash = i
						dots = 0
						continue
					}
				}
				if allowAboveRoot {
					if len(res) > 0 {
						res += "/.."
					} else {
						res = ".."
					}
					lastSegmentLength = 2
				}
			} else {
				if len(res) > 0 {
					res += "/" + path[lastSlash+1:i]
				} else {
					res = path[lastSlash+1 : i]
				}
				lastSegmentLength = i - lastSlash - 1
			}
			lastSlash = i
			dots = 0
		} else if code == '.' && dots != -1 {
			dots++
		} else {
			dots = -1
		}
	}
	return res
}

// posixDirname follows Node's path.posix.dirname.
func posixDirname(path string) string {
	if path == "" {
		return "."
	}
	hasRoot := path[0] == '/'
	end := -1
	matchedSlash := true
	for i := len(path) - 1; i >= 1; i-- {
		if path[i] == '/' {
			if !matchedSlash {
				end = i
				break
			}
		} else {
			matchedSlash = false
		}
	}
	if end == -1 {
		if hasRoot {
			return "/"
		}
		return "."
	}
	if hasRoot && end == 1 {
		return "//"
	}
	return path[:end]
}

// posixBasename follows Node's path.posix.basename without a suffix argument.
func posixBasename(path string) string {
	start := 0
	end := -1
	matchedSlash := true
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if !matchedSlash {
				start = i + 1
				break
			}
		} else if end == -1 {
			matchedSlash = false
			end = i + 1
		}
	}
	if end == -1 {
		return ""
	}
	return path[start:end]
}
