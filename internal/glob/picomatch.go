package glob

import (
	"errors"
	"sort"
	"strconv"
)

// This file builds glob regexes as picomatch 2.3.2 lib/parse.js does (posix constants, the options glob-stream
// forwards). Strings are []rune slices of UTF-16 code units so that lengths, indexes and slices agree with the
// JavaScript original.

const maxPatternLength = 1024 * 64

var (
	rDotLiteral   = []rune(`\.`)
	rPlusLiteral  = []rune(`\+`)
	rSlashLiteral = []rune(`\/`)
	rOneChar      = []rune(`(?=.)`)
	rQmark        = []rune(`[^/]`)
	rDotsSlash    = []rune(`\.{1,2}(?:\/|$)`)
	rNoDot        = []rune(`(?!\.)`)
	rNoDots       = []rune(`(?!(?:^|\/)\.{1,2}(?:\/|$))`)
	rNoDotSlash   = []rune(`(?!\.{0,1}(?:\/|$))`)
	rNoDotsSlash  = []rune(`(?!\.{1,2}(?:\/|$))`)
	rQmarkNoDot   = []rune(`[^.\/]`)
	rStar         = []rune(`[^/]*?`)
	rStartAnchor  = []rune(`(?:^|\/)`)
)

var posixRegexSource = map[string]string{
	"alnum":  "a-zA-Z0-9",
	"alpha":  "a-zA-Z",
	"ascii":  `\x00-\x7F`,
	"blank":  ` \t`,
	"cntrl":  `\x00-\x1F\x7F`,
	"digit":  "0-9",
	"graph":  `\x21-\x7E`,
	"lower":  "a-z",
	"print":  `\x20-\x7E `,
	"punct":  `\-!"#$%&'()\*+,./:;<=>?@[\]^_` + "`" + `{|}~`,
	"space":  ` \t\r\n\v\f`,
	"upper":  "A-Z",
	"word":   "A-Za-z0-9_",
	"xdigit": "A-Fa-f0-9",
}

var replacements = map[string]string{
	"***":      "*",
	"**/**":    "**",
	"**/**/**": "**",
}

type extglobChars struct {
	typ         string
	open, close []rune
}

func extglobFor(c rune) extglobChars {
	switch c {
	case '!':
		return extglobChars{"negate", []rune("(?:(?!(?:"), cat([]rune("))"), rStar, []rune(")"))}
	case '?':
		return extglobChars{"qmark", []rune("(?:"), []rune(")?")}
	case '+':
		return extglobChars{"plus", []rune("(?:"), []rune(")+")}
	case '*':
		return extglobChars{"star", []rune("(?:"), []rune(")*")}
	}
	return extglobChars{"at", []rune("(?:"), []rune(")")}
}

// pmOptions are the picomatch options this package honours.
type pmOptions struct {
	dot        bool
	nobrace    bool
	noglobstar bool
	noextglob  bool
}

// toUnits converts a Go string to UTF-16 code units held in a rune slice.
func toUnits(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			out = append(out, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		out = append(out, r)
	}
	return out
}

func cat(parts ...[]rune) []rune {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]rune, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func eq(a []rune, s string) bool {
	if len(a) != len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if a[i] != rune(s[i]) {
			return false
		}
	}
	return true
}

func equalUnits(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasPrefix(a []rune, s string) bool {
	return len(a) >= len(s) && eq(a[:len(s)], s)
}

func indexRune(a []rune, c rune) int {
	for i, x := range a {
		if x == c {
			return i
		}
	}
	return -1
}

func containsRune(a []rune, c rune) bool { return indexRune(a, c) >= 0 }

func lastIndexRune(a []rune, c rune) int {
	for i := len(a) - 1; i >= 0; i-- {
		if a[i] == c {
			return i
		}
	}
	return -1
}

// jsSlice follows String.prototype.slice(start, end) with negative indexes.
func jsSlice(a []rune, start, end int) []rune {
	n := len(a)
	if start < 0 {
		start += n
		if start < 0 {
			start = 0
		}
	}
	if start > n {
		start = n
	}
	if end < 0 {
		end += n
		if end < 0 {
			end = 0
		}
	}
	if end > n {
		end = n
	}
	if start >= end {
		return []rune{}
	}
	return append([]rune(nil), a[start:end]...)
}

// dropLast mirrors s.slice(0, -n), where n == 0 empties the string.
func dropLast(a []rune, n int) []rune {
	return jsSlice(a, 0, -n)
}

func isRegexSpecial(c rune) bool {
	switch c {
	case '-', '*', '+', '?', '.', '^', '$', '{', '}', '(', '|', ')', '[', ']':
		return true
	}
	return false
}

func hasRegexChars(a []rune) bool {
	for _, c := range a {
		if isRegexSpecial(c) {
			return true
		}
	}
	return false
}

func escapeRegex(a []rune) []rune {
	out := make([]rune, 0, len(a))
	for _, c := range a {
		if isRegexSpecial(c) {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return out
}

func jsTrim(a []rune) []rune {
	i, j := 0, len(a)
	for i < j && isJSSpace(a[i]) {
		i++
	}
	for j > i && isJSSpace(a[j-1]) {
		j--
	}
	return a[i:j]
}

// escapeLast is utils.escapeLast: it escapes the last unescaped occurrence of char at or before lastIdx.
func escapeLast(input []rune, char rune, lastIdx int) []rune {
	idx := -1
	from := lastIdx
	if from >= len(input) {
		from = len(input) - 1
	}
	for i := from; i >= 0; i-- {
		if input[i] == char {
			idx = i
			break
		}
	}
	if idx == -1 {
		return input
	}
	if idx > 0 && input[idx-1] == '\\' {
		return escapeLast(input, char, idx-1)
	}
	return cat(input[:idx], []rune{'\\'}, input[idx:])
}

func removePrefix(input []rune) ([]rune, bool) {
	if hasPrefix(input, "./") {
		return input[2:], true
	}
	return input, false
}

// wrapOutput is utils.wrapOutput for a non-negated state.
func wrapOutput(input []rune) []rune {
	return cat([]rune("^(?:"), input, []rune(")$"))
}

type pmToken struct {
	typ         string
	value       []rune
	output      []rune
	hasOutput   bool
	prev        *pmToken
	extglob     bool
	star        bool
	posix       bool
	comma       bool
	dots        bool
	outputIndex int
	tokensIndex int
}

func (t *pmToken) setOutput(o []rune) {
	t.output = o
	t.hasOutput = true
}

// outOrValue is `token.output != null ? token.output : token.value`.
func (t *pmToken) outOrValue() []rune {
	if t.hasOutput {
		return t.output
	}
	return t.value
}

// outString is the token's output as JavaScript string concatenation sees it (undefined prints as "undefined").
func (t *pmToken) outString() []rune {
	if t.hasOutput {
		return t.output
	}
	return []rune("undefined")
}

type extglobState struct {
	typ         string
	open, close []rune
	inner       []rune
	parens      int
	output      []rune
	startIndex  int
	tokensIndex int
}

type pmState struct {
	output  []rune
	negated bool
}

// errUndefinedType reproduces the TypeError picomatch throws when an extglob's opening token was already removed.
var errUndefinedType = errors.New("Cannot set properties of undefined (setting 'type')")

// errNoTermination replaces picomatch's endless parse loop for patterns whose index skips past the last unit.
var errNoTermination = errors.New("picomatch 2.3.2 never finishes parsing this pattern")

func lengthError(n int) error {
	return errors.New("Input length: " + strconv.Itoa(n) + ", exceeds maximum allowed length: " +
		strconv.Itoa(maxPatternLength))
}

// regexValid reports whether new RegExp(src) would compile.
func regexValid(src []rune) bool {
	_, err := compileJSRegexp(src, false)
	return err == nil
}

func expandRange(args [][]rune) []rune {
	sort.SliceStable(args, func(i, j int) bool { return lessUnits(args[i], args[j]) })
	var joined []rune
	for i, a := range args {
		if i > 0 {
			joined = append(joined, '-')
		}
		joined = append(joined, a...)
	}
	value := cat([]rune("["), joined, []rune("]"))
	if !regexValid(value) {
		var out []rune
		for i, a := range args {
			if i > 0 {
				out = append(out, '.', '.')
			}
			out = append(out, escapeRegex(a)...)
		}
		return out
	}
	return value
}

func lessUnits(a, b []rune) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// splitTopLevel splits an extglob body on top-level pipes.
func splitTopLevel(input []rune) [][]rune {
	var parts [][]rune
	bracket, paren, quote := 0, 0, 0
	var value []rune
	escaped := false
	for _, ch := range input {
		if escaped {
			value = append(value, ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			value = append(value, ch)
			escaped = true
			continue
		}
		if ch == '"' {
			if quote == 1 {
				quote = 0
			} else {
				quote = 1
			}
			value = append(value, ch)
			continue
		}
		if quote == 0 {
			if ch == '[' {
				bracket++
			} else if ch == ']' && bracket > 0 {
				bracket--
			} else if bracket == 0 {
				if ch == '(' {
					paren++
				} else if ch == ')' && paren > 0 {
					paren--
				} else if ch == '|' && paren == 0 {
					parts = append(parts, value)
					value = nil
					continue
				}
			}
		}
		value = append(value, ch)
	}
	parts = append(parts, value)
	return parts
}

func isPlainBranch(branch []rune) bool {
	escaped := false
	for _, ch := range branch {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		switch ch {
		case '?', '*', '+', '@', '!', '(', ')', '[', ']', '{', '}':
			return false
		}
	}
	return true
}

// isSimpleAtGroup tests /^@\([^\\()[\]{}|]+\)$/.
func isSimpleAtGroup(v []rune) bool {
	if len(v) < 4 || v[0] != '@' || v[1] != '(' || v[len(v)-1] != ')' {
		return false
	}
	for _, c := range v[2 : len(v)-1] {
		switch c {
		case '\\', '(', ')', '[', ']', '{', '}', '|':
			return false
		}
	}
	return true
}

// normalizeSimpleBranch returns nil where the JavaScript returns undefined.
func normalizeSimpleBranch(branch []rune) []rune {
	value := jsTrim(branch)
	for isSimpleAtGroup(value) {
		value = value[2 : len(value)-1]
	}
	if !isPlainBranch(value) {
		return nil
	}
	out := make([]rune, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) && !isLineTerminator(value[i+1]) {
			out = append(out, value[i+1])
			i++
			continue
		}
		out = append(out, value[i])
	}
	return out
}

func repeatedOf(a []rune, c rune) bool {
	for _, x := range a {
		if x != c {
			return false
		}
	}
	return true
}

func hasRepeatedCharPrefixOverlap(branches [][]rune) bool {
	var values [][]rune
	for _, b := range branches {
		if v := normalizeSimpleBranch(b); len(v) > 0 {
			values = append(values, v)
		}
	}
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			a, b := values[i], values[j]
			char := a[0]
			if !repeatedOf(a, char) || !repeatedOf(b, char) {
				continue
			}
			if equalUnits(a, b) || hasPrefixUnits(a, b) || hasPrefixUnits(b, a) {
				return true
			}
		}
	}
	return false
}

func hasPrefixUnits(a, p []rune) bool {
	return len(a) >= len(p) && equalUnits(a[:len(p)], p)
}

type repeatedMatch struct {
	typ  rune
	body []rune
	end  int
}

func parseRepeatedExtglob(pattern []rune, requireEnd bool) *repeatedMatch {
	if len(pattern) < 2 || (pattern[0] != '+' && pattern[0] != '*') || pattern[1] != '(' {
		return nil
	}
	bracket, paren, quote := 0, 0, 0
	escaped := false
	for i := 1; i < len(pattern); i++ {
		ch := pattern[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			if quote == 1 {
				quote = 0
			} else {
				quote = 1
			}
			continue
		}
		if quote == 1 {
			continue
		}
		if ch == '[' {
			bracket++
			continue
		}
		if ch == ']' && bracket > 0 {
			bracket--
			continue
		}
		if bracket > 0 {
			continue
		}
		if ch == '(' {
			paren++
			continue
		}
		if ch == ')' {
			paren--
			if paren == 0 {
				if requireEnd && i != len(pattern)-1 {
					return nil
				}
				return &repeatedMatch{typ: pattern[0], body: jsSlice(pattern, 2, i), end: i}
			}
		}
	}
	return nil
}

func getStarExtglobSequenceOutput(pattern []rune) []rune {
	index := 0
	var chars [][]rune
	for index < len(pattern) {
		match := parseRepeatedExtglob(pattern[index:], false)
		if match == nil || match.typ != '*' {
			return nil
		}
		branches := splitTopLevel(match.body)
		if len(branches) != 1 {
			return nil
		}
		branch := normalizeSimpleBranch(jsTrim(branches[0]))
		if len(branch) != 1 {
			return nil
		}
		chars = append(chars, branch)
		index += match.end + 1
	}
	if len(chars) < 1 {
		return nil
	}
	var source []rune
	if len(chars) == 1 {
		source = escapeRegex(chars[0])
	} else {
		source = []rune{'['}
		for _, c := range chars {
			source = append(source, escapeRegex(c)...)
		}
		source = append(source, ']')
	}
	return append(source, '*')
}

func repeatedExtglobRecursion(pattern []rune) int {
	depth := 0
	value := jsTrim(pattern)
	match := parseRepeatedExtglob(value, true)
	for match != nil {
		depth++
		value = jsTrim(match.body)
		match = parseRepeatedExtglob(value, true)
	}
	return depth
}

func isStarsOrQmarks(a []rune) bool {
	if len(a) == 0 {
		return false
	}
	for _, c := range a {
		if c != '*' && c != '?' {
			return false
		}
	}
	return true
}

// analyzeRepeatedExtglob reports whether a +() or *() body is risky and, when it can, a safe replacement.
func analyzeRepeatedExtglob(body []rune) (bool, []rune) {
	parts := splitTopLevel(body)
	branches := make([][]rune, len(parts))
	for i, b := range parts {
		branches[i] = jsTrim(b)
	}
	if len(branches) > 1 {
		for _, b := range branches {
			if len(b) == 0 || isStarsOrQmarks(b) {
				return true, nil
			}
		}
		if hasRepeatedCharPrefixOverlap(branches) {
			return true, nil
		}
	}
	for _, b := range branches {
		if safe := getStarExtglobSequenceOutput(b); safe != nil {
			return true, safe
		}
		if repeatedExtglobRecursion(b) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (o pmOptions) globstar() []rune {
	if o.dot {
		return cat([]rune("(?:(?:(?!"), rStartAnchor, rDotsSlash, []rune(").)*?)"))
	}
	return cat([]rune("(?:(?:(?!"), rStartAnchor, rDotLiteral, []rune(").)*?)"))
}

// isNonSpecial reports whether c belongs to picomatch's REGEX_NON_SPECIAL_CHARS class.
func isNonSpecial(c rune) bool {
	switch c {
	case '@', '!', '[', ']', '.', ',', '$', '*', '+', '?', '^', '{', '}', '(', ')', '|', '\\', '/':
		return false
	}
	return true
}

// fastPathReplace is the REGEX_SPECIAL_CHARS_BACKREF replacement of parse()'s fast path.
func fastPathReplace(input []rune, qmarkNoDot []rune) []rune {
	var out []rune
	backslashes := false
	i := 0
	for i < len(input) {
		var esc bool
		var first rune
		var j int
		if input[i] == '\\' && i+1 < len(input) && !isWordUnit(input[i+1]) {
			esc = true
			first = input[i+1]
			j = i + 2
		} else if !isWordUnit(input[i]) {
			first = input[i]
			j = i + 1
		} else {
			out = append(out, input[i])
			i++
			continue
		}
		for j < len(input) && input[j] == first {
			j++
		}
		m := input[i:j]
		restLen := j - i - 1
		if esc {
			restLen--
		}
		chars := m
		if esc {
			chars = m[1:]
		}
		switch {
		case first == '\\':
			backslashes = true
			out = append(out, m...)
		case first == '?':
			if esc {
				out = append(out, '\\', '?')
				for x := 0; x < restLen; x++ {
					out = append(out, rQmark...)
				}
			} else if i == 0 {
				out = append(out, qmarkNoDot...)
				for x := 0; x < restLen; x++ {
					out = append(out, rQmark...)
				}
			} else {
				for x := 0; x < len(chars); x++ {
					out = append(out, rQmark...)
				}
			}
		case first == '.':
			for x := 0; x < len(chars); x++ {
				out = append(out, rDotLiteral...)
			}
		case first == '*':
			if esc {
				out = append(out, '\\', '*')
				if restLen > 0 {
					out = append(out, rStar...)
				}
			} else {
				out = append(out, rStar...)
			}
		default:
			if !esc {
				out = append(out, '\\')
			}
			out = append(out, m...)
		}
		i = j
	}
	if backslashes {
		var res []rune
		for k := 0; k < len(out); {
			if out[k] != '\\' {
				res = append(res, out[k])
				k++
				continue
			}
			e := k
			for e < len(out) && out[e] == '\\' {
				e++
			}
			if (e-k)%2 == 0 {
				res = append(res, '\\', '\\')
			} else {
				res = append(res, '\\')
			}
			k = e
		}
		out = res
	}
	return out
}

// fastPathEligible is !/(^[*!]|[/()[\]{}"])/.test(input).
func fastPathEligible(input []rune) bool {
	if len(input) > 0 && (input[0] == '*' || input[0] == '!') {
		return false
	}
	for _, c := range input {
		switch c {
		case '/', '(', ')', '[', ']', '{', '}', '"':
			return false
		}
	}
	return true
}

// splitExtension matches /^(.*?)\.(\w+)$/ and returns the two groups.
func splitExtension(str []rune) ([]rune, []rune, bool) {
	dot := lastIndexRune(str, '.')
	if dot < 0 || dot == len(str)-1 {
		return nil, nil, false
	}
	for _, c := range str[dot+1:] {
		if !isWordUnit(c) {
			return nil, nil, false
		}
	}
	for _, c := range str[:dot] {
		if isLineTerminator(c) {
			return nil, nil, false
		}
	}
	return str[:dot], str[dot+1:], true
}

// parseFastpaths is parse.fastpaths; it returns nil where the JavaScript returns undefined.
func parseFastpaths(input []rune, o pmOptions) ([]rune, error) {
	if len(input) > maxPatternLength {
		return nil, lengthError(len(input))
	}
	if r, ok := replacementsByString(input); ok {
		input = r
	}
	nodot := rNoDot
	slashDot := rNoDot
	if o.dot {
		nodot = rNoDots
		slashDot = rNoDotsSlash
	}
	star := rStar
	globstar := func() []rune {
		if o.noglobstar {
			return star
		}
		return o.globstar()
	}
	var create func(str []rune) []rune
	create = func(str []rune) []rune {
		switch {
		case eq(str, "*"):
			return cat(nodot, rOneChar, star)
		case eq(str, ".*"):
			return cat(rDotLiteral, rOneChar, star)
		case eq(str, "*.*"):
			return cat(nodot, star, rDotLiteral, rOneChar, star)
		case eq(str, "*/*"):
			return cat(nodot, star, rSlashLiteral, rOneChar, slashDot, star)
		case eq(str, "**"):
			return cat(nodot, globstar())
		case eq(str, "**/*"):
			return cat([]rune("(?:"), nodot, globstar(), rSlashLiteral, []rune(")?"), slashDot, rOneChar, star)
		case eq(str, "**/*.*"):
			return cat([]rune("(?:"), nodot, globstar(), rSlashLiteral, []rune(")?"), slashDot, star, rDotLiteral,
				rOneChar, star)
		case eq(str, "**/.*"):
			return cat([]rune("(?:"), nodot, globstar(), rSlashLiteral, []rune(")?"), rDotLiteral, rOneChar, star)
		}
		head, ext, ok := splitExtension(str)
		if !ok {
			return nil
		}
		source := create(head)
		if source == nil {
			return nil
		}
		return cat(source, rDotLiteral, ext)
	}
	output, _ := removePrefix(input)
	source := create(output)
	if source != nil {
		source = cat(source, rSlashLiteral, []rune("?"))
	}
	return source, nil
}

func replacementsByString(input []rune) ([]rune, bool) {
	for k, v := range replacements {
		if eq(input, k) {
			return []rune(v), true
		}
	}
	return nil, false
}

// parser holds the state of one parse() call.
type parser struct {
	o         pmOptions
	input     []rune
	index     int
	start     int
	output    []rune
	backtrack bool
	negated   bool
	brackets  int
	braces    int
	parens    int
	quotes    int
	tokens    []*pmToken
	bos       *pmToken
	prev      *pmToken
	extglobs  []*extglobState
	braceStk  []*pmToken
	stack     []string
	star      []rune
	err       error
}

func (p *parser) eos() bool { return p.index == len(p.input)-1 }

func (p *parser) peek(n int) rune {
	if i := p.index + n; i >= 0 && i < len(p.input) {
		return p.input[i]
	}
	return -1
}

// advance returns the next unit, or -1 where the JavaScript returns an empty string.
func (p *parser) advance() rune {
	p.index++
	if p.index < len(p.input) {
		return p.input[p.index]
	}
	return -1
}

func (p *parser) remaining() []rune {
	if p.index+1 >= len(p.input) {
		return nil
	}
	return p.input[p.index+1:]
}

func (p *parser) increment(typ string) {
	switch typ {
	case "parens":
		p.parens++
	case "brackets":
		p.brackets++
	case "braces":
		p.braces++
	}
	p.stack = append(p.stack, typ)
}

func (p *parser) decrement(typ string) {
	switch typ {
	case "parens":
		p.parens--
	case "brackets":
		p.brackets--
	case "braces":
		p.braces--
	}
	if len(p.stack) > 0 {
		p.stack = p.stack[:len(p.stack)-1]
	}
}

func (p *parser) appendTok(t *pmToken) {
	p.output = cat(p.output, t.outOrValue())
}

func (p *parser) push(tok *pmToken) {
	prev := p.prev
	if prev.typ == "globstar" {
		isBrace := p.braces > 0 && (tok.typ == "comma" || tok.typ == "brace")
		isExtglob := tok.extglob || (len(p.extglobs) > 0 && (tok.typ == "pipe" || tok.typ == "paren"))
		if tok.typ != "slash" && tok.typ != "paren" && !isBrace && !isExtglob {
			p.output = dropLast(p.output, len(prev.outString()))
			prev.typ = "star"
			prev.value = []rune("*")
			prev.setOutput(p.star)
			p.output = cat(p.output, prev.output)
		}
	}
	if len(p.extglobs) > 0 && tok.typ != "paren" {
		e := p.extglobs[len(p.extglobs)-1]
		e.inner = cat(e.inner, tok.value)
	}
	if len(tok.value) > 0 || (tok.hasOutput && len(tok.output) > 0) {
		p.appendTok(tok)
	}
	if prev != nil && prev.typ == "text" && tok.typ == "text" {
		prev.value = cat(prev.value, tok.value)
		base := prev.output
		if !prev.hasOutput {
			base = nil
		}
		prev.setOutput(cat(base, tok.value))
		return
	}
	tok.prev = prev
	p.tokens = append(p.tokens, tok)
	p.prev = tok
}

func text(v []rune) *pmToken { return &pmToken{typ: "text", value: v} }

func textOut(v, out []rune) *pmToken {
	return &pmToken{typ: "text", value: v, output: out, hasOutput: true}
}

func (p *parser) extglobOpen(typ string, value rune) {
	ec := extglobFor(value)
	tok := &extglobState{typ: ec.typ, open: ec.open, close: ec.close}
	tok.parens = p.parens
	tok.output = p.output
	tok.startIndex = p.index
	tok.tokensIndex = len(p.tokens)
	output := tok.open
	p.increment("parens")
	first := rOneChar
	if len(p.output) > 0 {
		first = []rune{}
	}
	p.push(&pmToken{typ: typ, value: []rune{value}, output: first, hasOutput: true})
	next := p.advance()
	var nv []rune
	if next >= 0 {
		nv = []rune{next}
	}
	p.push(&pmToken{typ: "paren", extglob: true, value: nv, output: output, hasOutput: true})
	p.extglobs = append(p.extglobs, tok)
}

func (p *parser) extglobClose(tok *extglobState, value []rune) {
	literal := jsSlice(p.input, tok.startIndex, p.index+1)
	body := jsSlice(p.input, tok.startIndex+2, p.index)
	risky, safe := analyzeRepeatedExtglob(body)
	if (tok.typ == "plus" || tok.typ == "star") && risky {
		var safeOutput []rune
		if safe != nil {
			if len(tok.output) == 0 {
				safeOutput = cat(rOneChar, safe)
			} else {
				safeOutput = safe
			}
		}
		if tok.tokensIndex >= len(p.tokens) {
			p.err = errUndefinedType
			return
		}
		open := p.tokens[tok.tokensIndex]
		open.typ = "text"
		open.value = literal
		if len(safeOutput) > 0 {
			open.setOutput(safeOutput)
		} else {
			open.setOutput(escapeRegex(literal))
		}
		for i := tok.tokensIndex + 1; i < len(p.tokens); i++ {
			p.tokens[i].value = []rune{}
			p.tokens[i].setOutput([]rune{})
		}
		p.output = cat(tok.output, open.output)
		p.backtrack = true
		p.push(&pmToken{typ: "paren", extglob: true, value: value, output: []rune{}, hasOutput: true})
		p.decrement("parens")
		return
	}
	output := tok.close
	if tok.typ == "negate" {
		extglobStar := p.star
		gs := p.o.globstar()
		usesGlobstar := false
		if len(tok.inner) > 1 && containsRune(tok.inner, '/') {
			extglobStar = gs
			usesGlobstar = true
		}
		rem := p.remaining()
		if usesGlobstar || p.eos() || allCloseParens(rem) {
			output = cat([]rune(")$))"), extglobStar)
			tok.close = output
		}
		if containsRune(tok.inner, '*') && len(rem) > 0 && isDotExtension(rem) {
			sub, err := parse(rem, p.o, false)
			if err != nil {
				p.err = err
				return
			}
			output = cat([]rune(")"), sub.output, []rune(")"), extglobStar, []rune(")"))
			tok.close = output
		}
	}
	p.push(&pmToken{typ: "paren", extglob: true, value: value, output: output, hasOutput: true})
	p.decrement("parens")
}

// allCloseParens tests /^\)+$/.
func allCloseParens(a []rune) bool {
	if len(a) == 0 {
		return false
	}
	for _, c := range a {
		if c != ')' {
			return false
		}
	}
	return true
}

// isDotExtension tests /^\.[^\\/.]+$/.
func isDotExtension(a []rune) bool {
	if len(a) < 2 || a[0] != '.' {
		return false
	}
	for _, c := range a[1:] {
		if c == '\\' || c == '/' || c == '.' {
			return false
		}
	}
	return true
}

// lookbehindOrName tests /<([!=]|\w+>)/ against s.
func lookbehindOrName(s []rune) bool {
	for i, c := range s {
		if c != '<' {
			continue
		}
		if i+1 < len(s) && (s[i+1] == '!' || s[i+1] == '=') {
			return true
		}
		j := i + 1
		for j < len(s) && isWordUnit(s[j]) {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == '>' {
			return true
		}
	}
	return false
}

func isLookChar(c rune) bool { return c == '!' || c == '=' || c == '<' || c == ':' }

// parse follows picomatch's parse(input, options); fastpaths selects the options.fastpaths !== false branch.
func parse(input []rune, o pmOptions, fastpaths bool) (*pmState, error) {
	if r, ok := replacementsByString(input); ok {
		input = r
	}
	if len(input) > maxPatternLength {
		return nil, lengthError(len(input))
	}
	bos := &pmToken{typ: "bos", value: []rune{}, output: []rune{}, hasOutput: true}
	p := &parser{o: o, tokens: []*pmToken{bos}, bos: bos, prev: bos, star: rStar}
	nodot := rNoDot
	qmarkNoDot := rQmarkNoDot
	if o.dot {
		nodot = []rune{}
		qmarkNoDot = rQmark
	}
	input, _ = removePrefix(input)
	p.input = input
	p.index = -1

	if fastpaths && fastPathEligible(input) {
		out := fastPathReplace(input, qmarkNoDot)
		return &pmState{output: wrapOutput(out)}, nil
	}

	for !p.eos() {
		if p.err != nil {
			return nil, p.err
		}
		if p.index >= len(p.input) {
			// A run of backslashes near the end can move the index past eos(); picomatch then spins forever.
			return nil, errNoTermination
		}
		value := []rune{p.advance()}

		if value[0] == 0 {
			continue
		}

		if value[0] == '\\' {
			next := p.peek(1)
			if next == '/' {
				continue
			}
			if next == '.' || next == ';' {
				continue
			}
			if next == -1 {
				value = []rune(`\\`)
				p.push(text(value))
				continue
			}
			rem := p.remaining()
			slashes := 0
			for slashes < len(rem) && rem[slashes] == '\\' {
				slashes++
			}
			if slashes > 2 {
				p.index += slashes
				if slashes%2 != 0 {
					value = append(value, '\\')
				}
			} else {
				slashes = 0
			}
			if a := p.advance(); a >= 0 {
				value = append(value, a)
			}
			if p.brackets == 0 {
				p.push(text(value))
				continue
			}
		}

		prev := p.prev
		if p.brackets > 0 && (!eq(value, "]") || eq(prev.value, "[") || eq(prev.value, "[^")) {
			if eq(value, ":") {
				inner := jsSlice(prev.value, 1, len(prev.value))
				if containsRune(inner, '[') {
					prev.posix = true
					if containsRune(inner, ':') {
						idx := lastIndexRune(prev.value, '[')
						pre := jsSlice(prev.value, 0, idx)
						rest := jsSlice(prev.value, idx+2, len(prev.value))
						if posix, ok := posixRegexSource[string(rest)]; ok && isASCII(rest) {
							prev.value = cat(pre, []rune(posix))
							p.backtrack = true
							p.advance()
							if len(bos.output) == 0 && p.tokenIndex(prev) == 1 {
								bos.setOutput(rOneChar)
							}
							continue
						}
					}
				}
			}
			if (eq(value, "[") && p.peek(1) != ':') || (eq(value, "-") && p.peek(1) == ']') {
				value = cat([]rune{'\\'}, value)
			}
			if eq(value, "]") && (eq(prev.value, "[") || eq(prev.value, "[^")) {
				value = cat([]rune{'\\'}, value)
			}
			prev.value = cat(prev.value, value)
			p.output = cat(p.output, value)
			continue
		}

		if p.quotes == 1 && !eq(value, `"`) {
			value = escapeRegex(value)
			prev.value = cat(prev.value, value)
			p.output = cat(p.output, value)
			continue
		}

		if eq(value, `"`) {
			if p.quotes == 1 {
				p.quotes = 0
			} else {
				p.quotes = 1
			}
			continue
		}

		if eq(value, "(") {
			p.increment("parens")
			p.push(&pmToken{typ: "paren", value: value})
			continue
		}

		if eq(value, ")") {
			if n := len(p.extglobs); n > 0 && p.parens == p.extglobs[n-1].parens+1 {
				e := p.extglobs[n-1]
				p.extglobs = p.extglobs[:n-1]
				p.extglobClose(e, value)
				continue
			}
			out := []rune(`\)`)
			if p.parens != 0 {
				out = []rune(")")
			}
			p.push(&pmToken{typ: "paren", value: value, output: out, hasOutput: true})
			p.decrement("parens")
			continue
		}

		if eq(value, "[") {
			if !containsRune(p.remaining(), ']') {
				value = []rune(`\[`)
			} else {
				p.increment("brackets")
			}
			p.push(&pmToken{typ: "bracket", value: value})
			continue
		}

		if eq(value, "]") {
			if prev != nil && prev.typ == "bracket" && len(prev.value) == 1 {
				p.push(textOut(value, []rune(`\]`)))
				continue
			}
			if p.brackets == 0 {
				p.push(textOut(value, []rune(`\]`)))
				continue
			}
			p.decrement("brackets")
			prevValue := jsSlice(prev.value, 1, len(prev.value))
			if !prev.posix && len(prevValue) > 0 && prevValue[0] == '^' && !containsRune(prevValue, '/') {
				value = []rune("/]")
			}
			prev.value = cat(prev.value, value)
			p.output = cat(p.output, value)
			if hasRegexChars(prevValue) {
				continue
			}
			escaped := escapeRegex(prev.value)
			p.output = dropLast(p.output, len(prev.value))
			prev.value = cat([]rune("(?:"), escaped, []rune("|"), prev.value, []rune(")"))
			p.output = cat(p.output, prev.value)
			continue
		}

		if eq(value, "{") && !o.nobrace {
			p.increment("braces")
			open := &pmToken{typ: "brace", value: value, output: []rune("("), hasOutput: true,
				outputIndex: len(p.output), tokensIndex: len(p.tokens)}
			p.braceStk = append(p.braceStk, open)
			p.push(open)
			continue
		}

		if eq(value, "}") {
			if o.nobrace || len(p.braceStk) == 0 {
				p.push(&pmToken{typ: "text", value: value, output: value, hasOutput: true})
				continue
			}
			brace := p.braceStk[len(p.braceStk)-1]
			output := []rune(")")
			if brace.dots {
				arr := append([]*pmToken(nil), p.tokens...)
				var rng [][]rune
				for i := len(arr) - 1; i >= 0; i-- {
					p.tokens = p.tokens[:len(p.tokens)-1]
					if arr[i].typ == "brace" {
						break
					}
					if arr[i].typ != "dots" {
						rng = append([][]rune{arr[i].value}, rng...)
					}
				}
				output = expandRange(rng)
				p.backtrack = true
			}
			if !brace.comma && !brace.dots {
				out := jsSlice(p.output, 0, brace.outputIndex)
				var toks []*pmToken
				if brace.tokensIndex < len(p.tokens) {
					toks = p.tokens[brace.tokensIndex:]
				}
				brace.value = []rune(`\{`)
				brace.setOutput([]rune(`\{`))
				value = []rune(`\}`)
				output = []rune(`\}`)
				p.output = out
				for _, t := range toks {
					if t.hasOutput && len(t.output) > 0 {
						p.output = cat(p.output, t.output)
					} else {
						p.output = cat(p.output, t.value)
					}
				}
			}
			p.push(&pmToken{typ: "brace", value: value, output: output, hasOutput: true})
			p.decrement("braces")
			p.braceStk = p.braceStk[:len(p.braceStk)-1]
			continue
		}

		if eq(value, "|") {
			p.push(text(value))
			continue
		}

		if eq(value, ",") {
			output := value
			if len(p.braceStk) > 0 && len(p.stack) > 0 && p.stack[len(p.stack)-1] == "braces" {
				p.braceStk[len(p.braceStk)-1].comma = true
				output = []rune("|")
			}
			p.push(&pmToken{typ: "comma", value: value, output: output, hasOutput: true})
			continue
		}

		if eq(value, "/") {
			if prev.typ == "dot" && p.index == p.start+1 {
				p.start = p.index + 1
				p.output = []rune{}
				p.tokens = p.tokens[:len(p.tokens)-1]
				p.prev = bos
				continue
			}
			p.push(&pmToken{typ: "slash", value: value, output: rSlashLiteral, hasOutput: true})
			continue
		}

		if eq(value, ".") {
			if p.braces > 0 && prev.typ == "dot" {
				if eq(prev.value, ".") {
					prev.setOutput(rDotLiteral)
				}
				brace := p.braceStk[len(p.braceStk)-1]
				prev.typ = "dots"
				prev.setOutput(cat(prev.outString(), value))
				prev.value = cat(prev.value, value)
				brace.dots = true
				continue
			}
			if p.braces+p.parens == 0 && prev.typ != "bos" && prev.typ != "slash" {
				p.push(textOut(value, rDotLiteral))
				continue
			}
			p.push(&pmToken{typ: "dot", value: value, output: rDotLiteral, hasOutput: true})
			continue
		}

		if eq(value, "?") {
			isGroup := prev != nil && eq(prev.value, "(")
			if !isGroup && !o.noextglob && p.peek(1) == '(' && p.peek(2) != '?' {
				p.extglobOpen("qmark", '?')
				continue
			}
			if prev != nil && prev.typ == "paren" {
				next := p.peek(1)
				output := value
				if (eq(prev.value, "(") && !isLookChar(next)) || (next == '<' && !lookbehindOrName(p.remaining())) {
					output = []rune(`\?`)
				}
				p.push(textOut(value, output))
				continue
			}
			if !o.dot && (prev.typ == "slash" || prev.typ == "bos") {
				p.push(&pmToken{typ: "qmark", value: value, output: rQmarkNoDot, hasOutput: true})
				continue
			}
			p.push(&pmToken{typ: "qmark", value: value, output: rQmark, hasOutput: true})
			continue
		}

		if eq(value, "!") {
			if !o.noextglob && p.peek(1) == '(' {
				if p.peek(2) != '?' || !isLookChar(p.peek(3)) {
					p.extglobOpen("negate", '!')
					continue
				}
			}
			if p.index == 0 {
				p.negate()
				continue
			}
		}

		if eq(value, "+") {
			if !o.noextglob && p.peek(1) == '(' && p.peek(2) != '?' {
				p.extglobOpen("plus", '+')
				continue
			}
			if prev != nil && eq(prev.value, "(") {
				p.push(&pmToken{typ: "plus", value: value, output: rPlusLiteral, hasOutput: true})
				continue
			}
			if (prev != nil && (prev.typ == "bracket" || prev.typ == "paren" || prev.typ == "brace")) || p.parens > 0 {
				p.push(&pmToken{typ: "plus", value: value})
				continue
			}
			p.push(&pmToken{typ: "plus", value: rPlusLiteral})
			continue
		}

		if eq(value, "@") {
			if !o.noextglob && p.peek(1) == '(' && p.peek(2) != '?' {
				p.push(&pmToken{typ: "at", extglob: true, value: value, output: []rune{}, hasOutput: true})
				continue
			}
			p.push(text(value))
			continue
		}

		if !eq(value, "*") {
			if eq(value, "$") || eq(value, "^") {
				value = cat([]rune{'\\'}, value)
			}
			rem := p.remaining()
			n := 0
			for n < len(rem) && isNonSpecial(rem[n]) {
				n++
			}
			if n > 0 {
				value = cat(value, rem[:n])
				p.index += n
			}
			p.push(text(value))
			continue
		}

		// stars
		if prev != nil && (prev.typ == "globstar" || prev.star) {
			prev.typ = "star"
			prev.star = true
			prev.value = cat(prev.value, value)
			prev.setOutput(p.star)
			p.backtrack = true
			continue
		}

		rest := p.remaining()
		if !o.noextglob && len(rest) >= 2 && rest[0] == '(' && rest[1] != '?' {
			p.extglobOpen("star", '*')
			continue
		}

		if prev.typ == "star" {
			if o.noglobstar {
				continue
			}
			prior := prev.prev
			before := prior.prev
			isStart := prior.typ == "slash" || prior.typ == "bos"
			afterStar := before != nil && (before.typ == "star" || before.typ == "globstar")

			isBrace := p.braces > 0 && (prior.typ == "comma" || prior.typ == "brace")
			isExtglob := len(p.extglobs) > 0 && (prior.typ == "pipe" || prior.typ == "paren")
			if !isStart && prior.typ != "paren" && !isBrace && !isExtglob {
				p.push(&pmToken{typ: "star", value: value, output: []rune{}, hasOutput: true})
				continue
			}

			for hasPrefix(rest, "/**") {
				after := p.peek(4)
				if after >= 0 && after != '/' {
					break
				}
				rest = rest[3:]
				p.index += 3
			}

			if prior.typ == "bos" && p.eos() {
				prev.typ = "globstar"
				prev.value = cat(prev.value, value)
				prev.setOutput(o.globstar())
				p.output = prev.output
				continue
			}

			if prior.typ == "slash" && prior.prev != nil && prior.prev.typ != "bos" && !afterStar && p.eos() {
				p.output = dropLast(p.output, len(prior.outString())+len(prev.outString()))
				prior.setOutput(cat([]rune("(?:"), prior.outString()))
				prev.typ = "globstar"
				prev.setOutput(cat(o.globstar(), []rune("|$)")))
				prev.value = cat(prev.value, value)
				p.output = cat(p.output, prior.output, prev.output)
				continue
			}

			if prior.typ == "slash" && prior.prev != nil && prior.prev.typ != "bos" && len(rest) > 0 && rest[0] == '/' {
				end := []rune{}
				if len(rest) > 1 {
					end = []rune("|$")
				}
				p.output = dropLast(p.output, len(prior.outString())+len(prev.outString()))
				prior.setOutput(cat([]rune("(?:"), prior.outString()))
				prev.typ = "globstar"
				prev.setOutput(cat(o.globstar(), rSlashLiteral, []rune("|"), rSlashLiteral, end, []rune(")")))
				prev.value = cat(prev.value, value)
				p.output = cat(p.output, prior.output, prev.output)
				p.advance()
				p.push(&pmToken{typ: "slash", value: []rune("/"), output: []rune{}, hasOutput: true})
				continue
			}

			if prior.typ == "bos" && len(rest) > 0 && rest[0] == '/' {
				prev.typ = "globstar"
				prev.value = cat(prev.value, value)
				prev.setOutput(cat([]rune("(?:^|"), rSlashLiteral, []rune("|"), o.globstar(), rSlashLiteral, []rune(")")))
				p.output = prev.output
				p.advance()
				p.push(&pmToken{typ: "slash", value: []rune("/"), output: []rune{}, hasOutput: true})
				continue
			}

			p.output = dropLast(p.output, len(prev.outString()))
			prev.typ = "globstar"
			prev.setOutput(o.globstar())
			prev.value = cat(prev.value, value)
			p.output = cat(p.output, prev.output)
			continue
		}

		tok := &pmToken{typ: "star", value: value, output: p.star, hasOutput: true}
		if p.index == p.start || prev.typ == "slash" || prev.typ == "dot" {
			var add []rune
			switch {
			case prev.typ == "dot":
				add = rNoDotSlash
			case o.dot:
				add = rNoDotsSlash
			default:
				add = nodot
			}
			if p.peek(1) != '*' {
				add = cat(add, rOneChar)
			}
			p.output = cat(p.output, add)
			prev.setOutput(cat(prev.outString(), add))
		}
		p.push(tok)
	}
	if p.err != nil {
		return nil, p.err
	}

	for p.brackets > 0 {
		p.output = escapeLast(p.output, '[', len(p.output))
		p.decrement("brackets")
	}
	for p.parens > 0 {
		p.output = escapeLast(p.output, '(', len(p.output))
		p.decrement("parens")
	}
	for p.braces > 0 {
		p.output = escapeLast(p.output, '{', len(p.output))
		p.decrement("braces")
	}

	if p.prev.typ == "star" || p.prev.typ == "bracket" {
		p.push(&pmToken{typ: "maybe_slash", value: []rune{}, output: cat(rSlashLiteral, []rune("?")), hasOutput: true})
	}

	if p.backtrack {
		p.output = []rune{}
		for _, t := range p.tokens {
			p.output = cat(p.output, t.outOrValue())
		}
	}
	return &pmState{output: p.output, negated: p.negated}, nil
}

func isASCII(a []rune) bool {
	for _, c := range a {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func (p *parser) tokenIndex(t *pmToken) int {
	for i, x := range p.tokens {
		if x == t {
			return i
		}
	}
	return -1
}

func (p *parser) negate() {
	count := 1
	for p.peek(1) == '!' && (p.peek(2) != '(' || p.peek(3) == '?') {
		p.advance()
		p.start++
		count++
	}
	if count%2 == 0 {
		return
	}
	p.negated = true
	p.start++
}

// makeRe follows picomatch.makeRe(input, options) followed by compileRe; it returns the regex source.
func makeRe(input []rune, o pmOptions) ([]rune, error) {
	var state *pmState
	if len(input) > 0 && (input[0] == '.' || input[0] == '*') {
		out, err := parseFastpaths(input, o)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 {
			state = &pmState{output: out}
		}
	}
	if state == nil {
		st, err := parse(input, o, true)
		if err != nil {
			return nil, err
		}
		state = st
	}
	source := cat([]rune("^(?:"), state.output, []rune(")$"))
	if state.negated {
		source = cat([]rune("^(?!"), source, []rune(").*$"))
	}
	return source, nil
}
