package glob

import (
	"errors"
	"sort"
	"sync"
	"unicode"
)

// This file holds a small backtracking engine for the ECMAScript regular expressions that picomatch builds. It
// follows the V8 parser in non-unicode (Annex B) mode and the ECMAScript matching semantics, working on UTF-16 code
// units, so that lookarounds, backreferences and case folding behave as they do in Node.js.

const reInf = 1<<31 - 1

type reKind int

const (
	reSeq reKind = iota
	reAlt
	reChar
	reDot
	reGroup
	reLook
	reBackref
	reAssert
	reQuant
)

type reNode struct {
	kind   reKind
	subs   []*reNode
	set    *charSet
	capIdx int // capture index for reGroup (0 means non-capturing)
	refs   []int
	refNm  string
	neg    bool
	behind bool
	min    int
	max    int
	greedy bool
	capLo  int
	capHi  int
	assert rune
}

type classItem struct {
	lo, hi rune
	esc    rune // one of d D s S w W, or 0 for a range
}

type charSet struct {
	negate bool
	items  []classItem
}

func isLineTerminator(c rune) bool {
	return c == '\n' || c == '\r' || c == 0x2028 || c == 0x2029
}

func isWordUnit(c rune) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isJSSpace(c rune) bool {
	switch {
	case c >= 0x09 && c <= 0x0D, c == 0x20, c == 0xA0, c == 0x1680, c >= 0x2000 && c <= 0x200A,
		c == 0x2028, c == 0x2029, c == 0x202F, c == 0x205F, c == 0x3000, c == 0xFEFF:
		return true
	}
	return false
}

func escMatch(esc rune, c rune) bool {
	switch esc {
	case 'd':
		return c >= '0' && c <= '9'
	case 'D':
		return !(c >= '0' && c <= '9')
	case 's':
		return isJSSpace(c)
	case 'S':
		return !isJSSpace(c)
	case 'w':
		return isWordUnit(c)
	case 'W':
		return !isWordUnit(c)
	}
	return false
}

func (cs *charSet) raw(c rune) bool {
	for _, it := range cs.items {
		if it.esc != 0 {
			if escMatch(it.esc, c) {
				return true
			}
		} else if c >= it.lo && c <= it.hi {
			return true
		}
	}
	return false
}

func (cs *charSet) match(c rune, icase bool) bool {
	found := false
	if !icase {
		found = cs.raw(c)
	} else {
		for _, x := range canonPreimage(canonicalize(c)) {
			if cs.raw(x) {
				found = true
				break
			}
		}
	}
	return found != cs.negate
}

var (
	canonOnce sync.Once
	canonMap  map[rune]rune
	canonInv  map[rune][]rune
)

func loadCanon() {
	canonMap = make(map[rune]rune, 1200)
	canonInv = make(map[rune][]rune, 1200)
	for _, r := range canonRuns {
		for c := r[0]; c <= r[1]; c += r[2] {
			canonMap[rune(c)] = rune(c + r[3])
		}
	}
	keys := make([]rune, 0, len(canonMap))
	for c := range canonMap {
		keys = append(keys, c)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, c := range keys {
		cc := canonMap[c]
		if _, ok := canonInv[cc]; !ok {
			if _, changed := canonMap[cc]; !changed {
				canonInv[cc] = []rune{cc}
			} else {
				canonInv[cc] = nil
			}
		}
		canonInv[cc] = append(canonInv[cc], c)
	}
}

// canonicalize is the ECMAScript Canonicalize operation for ignoreCase without the unicode flag.
func canonicalize(c rune) rune {
	canonOnce.Do(loadCanon)
	if cc, ok := canonMap[c]; ok {
		return cc
	}
	return c
}

// canonPreimage lists every code unit whose canonical value is cc.
func canonPreimage(cc rune) []rune {
	canonOnce.Do(loadCanon)
	if l, ok := canonInv[cc]; ok {
		return l
	}
	if _, changed := canonMap[cc]; changed {
		return nil
	}
	return []rune{cc}
}

// jsRegexp is a compiled ECMAScript regular expression.
type jsRegexp struct {
	m        mfunc
	ncap     int
	icase    bool
	anchored bool
}

type mstate struct {
	in    []rune
	caps  []int
	icase bool
}

type cont func(i int) bool

type mfunc func(s *mstate, i int, k cont) bool

var errRegexp = errors.New("invalid regular expression")

// compileJSRegexp parses src (UTF-16 code units) with the given ignoreCase flag. It returns errRegexp wherever V8
// would throw a SyntaxError.
func compileJSRegexp(src []rune, icase bool) (*jsRegexp, error) {
	p := &reParser{src: src}
	p.prescan()
	root, err := p.parseDisjunction()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.src) {
		// an unmatched ')'
		return nil, errRegexp
	}
	for _, br := range p.namedRefs {
		idx, ok := p.names[br.refNm]
		if !ok {
			return nil, errRegexp
		}
		br.refs = idx
	}
	re := &jsRegexp{ncap: p.capCount, icase: icase}
	re.m = compileNode(root, false)
	n := root
	for n.kind == reAlt && len(n.subs) == 1 {
		n = n.subs[0]
	}
	if n.kind == reSeq && len(n.subs) > 0 && n.subs[0].kind == reAssert && n.subs[0].assert == '^' {
		re.anchored = true
	} else if n.kind == reAssert && n.assert == '^' {
		re.anchored = true
	}
	return re, nil
}

// neverRegexp follows picomatch's fallback /$^/ for a source that does not compile.
func neverRegexp() *jsRegexp {
	re, _ := compileJSRegexp([]rune("$^"), false)
	return re
}

// test mirrors RegExp.prototype.test for a regex without the global flag.
func (re *jsRegexp) test(in []rune) bool {
	s := &mstate{in: in, caps: make([]int, 2*(re.ncap+1)), icase: re.icase}
	last := len(in)
	if re.anchored {
		last = 0
	}
	for start := 0; start <= last; start++ {
		for i := range s.caps {
			s.caps[i] = -1
		}
		if re.m(s, start, func(int) bool { return true }) {
			return true
		}
	}
	return false
}

type reParser struct {
	src       []rune
	pos       int
	totalCaps int
	hasNamed  bool
	capCount  int
	names     map[string][]int
	namePaths map[string][][]altStep
	namedRefs []*reNode
	path      []altStep
	nextDisj  int
}

type altStep struct{ disj, alt int }

func (p *reParser) cur() rune {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return -1
}

func (p *reParser) at(off int) rune {
	if p.pos+off < len(p.src) && p.pos+off >= 0 {
		return p.src[p.pos+off]
	}
	return -1
}

// prescan counts capture groups and detects named groups the way V8's ScanForCaptures does.
func (p *reParser) prescan() {
	inClass := false
	s := p.src
	for i := 0; i < len(s); {
		c := s[i]
		i++
		switch c {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if inClass {
				break
			}
			if i < len(s) && s[i] == '?' {
				if i+1 < len(s) && s[i+1] == '<' {
					if i+2 < len(s) && s[i+2] != '=' && s[i+2] != '!' {
						p.totalCaps++
						p.hasNamed = true
					}
				}
			} else {
				p.totalCaps++
			}
		}
	}
}

func (p *reParser) parseDisjunction() (*reNode, error) {
	disj := p.nextDisj
	p.nextDisj++
	alt := &reNode{kind: reAlt}
	idx := 0
	for {
		p.path = append(p.path, altStep{disj, idx})
		a, err := p.parseAlternative()
		p.path = p.path[:len(p.path)-1]
		if err != nil {
			return nil, err
		}
		alt.subs = append(alt.subs, a)
		if p.cur() == '|' {
			p.pos++
			idx++
			continue
		}
		break
	}
	return alt, nil
}

func (p *reParser) parseAlternative() (*reNode, error) {
	seq := &reNode{kind: reSeq}
	for p.pos < len(p.src) && p.cur() != '|' && p.cur() != ')' {
		t, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		if t != nil {
			seq.subs = append(seq.subs, t)
		}
	}
	return seq, nil
}

func isDigit(c rune) bool { return c >= '0' && c <= '9' }

func isHex(c rune) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c rune) rune {
	switch {
	case isDigit(c):
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

// parseInterval parses {n}, {n,} or {n,m} at the current position. It leaves pos unchanged when there is none.
func (p *reParser) parseInterval() (min, max int, ok bool) {
	start := p.pos
	if p.cur() != '{' {
		return 0, 0, false
	}
	p.pos++
	readNum := func() (int, bool) {
		if !isDigit(p.cur()) {
			return 0, false
		}
		n := 0
		for isDigit(p.cur()) {
			if n < reInf/10 {
				n = n*10 + int(p.cur()-'0')
			} else {
				n = reInf
			}
			p.pos++
		}
		if n > reInf {
			n = reInf
		}
		return n, true
	}
	n, okn := readNum()
	if !okn {
		p.pos = start
		return 0, 0, false
	}
	min, max = n, n
	if p.cur() == ',' {
		p.pos++
		if p.cur() == '}' {
			max = reInf
		} else {
			m, okm := readNum()
			if !okm {
				p.pos = start
				return 0, 0, false
			}
			max = m
		}
	}
	if p.cur() != '}' {
		p.pos = start
		return 0, 0, false
	}
	p.pos++
	return min, max, true
}

func (p *reParser) parseTerm() (*reNode, error) {
	c := p.cur()
	var atom *reNode
	quantifiable := true
	switch c {
	case '^', '$':
		p.pos++
		return &reNode{kind: reAssert, assert: c}, nil
	case '\\':
		switch p.at(1) {
		case -1:
			return nil, errRegexp
		case 'b', 'B':
			p.pos += 2
			return &reNode{kind: reAssert, assert: p.src[p.pos-1]}, nil
		}
		a, err := p.parseAtomEscape()
		if err != nil {
			return nil, err
		}
		atom = a
	case '(':
		a, behind, err := p.parseGroup()
		if err != nil {
			return nil, err
		}
		atom = a
		quantifiable = !behind
	case '[':
		cs, err := p.parseClass()
		if err != nil {
			return nil, err
		}
		atom = &reNode{kind: reChar, set: cs}
	case '*', '+', '?':
		return nil, errRegexp
	case '{':
		save := p.pos
		if _, _, ok := p.parseInterval(); ok {
			return nil, errRegexp
		}
		p.pos = save + 1
		atom = &reNode{kind: reChar, set: &charSet{items: []classItem{{lo: '{', hi: '{'}}}}
	case '.':
		p.pos++
		atom = &reNode{kind: reDot}
	default:
		p.pos++
		atom = &reNode{kind: reChar, set: &charSet{items: []classItem{{lo: c, hi: c}}}}
	}
	return p.parseQuantifier(atom, quantifiable)
}

func (p *reParser) parseQuantifier(atom *reNode, quantifiable bool) (*reNode, error) {
	var min, max int
	switch p.cur() {
	case '*':
		min, max = 0, reInf
		p.pos++
	case '+':
		min, max = 1, reInf
		p.pos++
	case '?':
		min, max = 0, 1
		p.pos++
	case '{':
		lo, hi, ok := p.parseInterval()
		if !ok {
			return atom, nil
		}
		if hi < lo {
			return nil, errRegexp
		}
		min, max = lo, hi
	default:
		return atom, nil
	}
	greedy := true
	if p.cur() == '?' {
		greedy = false
		p.pos++
	}
	if !quantifiable {
		return nil, errRegexp
	}
	return &reNode{kind: reQuant, subs: []*reNode{atom}, min: min, max: max, greedy: greedy,
		capLo: atom.capLo, capHi: atom.capHi}, nil
}

// parseGroup parses a parenthesised term. behind reports a lookbehind, which may not be quantified.
func (p *reParser) parseGroup() (*reNode, bool, error) {
	p.pos++ // (
	n := &reNode{kind: reGroup}
	behind := false
	name := ""
	named := false
	capture := true
	if p.cur() == '?' {
		capture = false
		switch p.at(1) {
		case ':':
			p.pos += 2
		case '=', '!':
			n.kind = reLook
			n.neg = p.at(1) == '!'
			p.pos += 2
		case '<':
			switch p.at(2) {
			case '=', '!':
				n.kind = reLook
				n.behind = true
				n.neg = p.at(2) == '!'
				behind = true
				p.pos += 3
			default:
				p.pos += 2
				nm, err := p.parseGroupName()
				if err != nil {
					return nil, false, err
				}
				name = nm
				named = true
				capture = true
			}
		default:
			return nil, false, errRegexp
		}
	}
	capStart := p.capCount + 1
	if capture {
		p.capCount++
		n.capIdx = p.capCount
		capStart = p.capCount
		if named {
			if p.names == nil {
				p.names = map[string][]int{}
				p.namePaths = map[string][][]altStep{}
			}
			for _, other := range p.namePaths[name] {
				if !altExclusive(other, p.path) {
					return nil, false, errRegexp
				}
			}
			p.names[name] = append(p.names[name], n.capIdx)
			p.namePaths[name] = append(p.namePaths[name], append([]altStep(nil), p.path...))
		}
	}
	body, err := p.parseDisjunction()
	if err != nil {
		return nil, false, err
	}
	if p.cur() != ')' {
		return nil, false, errRegexp
	}
	p.pos++
	n.subs = []*reNode{body}
	n.capLo = capStart
	n.capHi = p.capCount + 1
	return n, behind, nil
}

// altExclusive reports whether two alternative paths lie in different branches of a shared disjunction.
func altExclusive(a, b []altStep) bool {
	for _, x := range a {
		for _, y := range b {
			if x.disj == y.disj && x.alt != y.alt {
				return true
			}
		}
	}
	return false
}

func isIDStart(c rune) bool {
	return c == '$' || c == '_' || unicode.IsLetter(c) || unicode.Is(unicode.Nl, c) ||
		unicode.Is(unicode.Other_ID_Start, c)
}

func isIDContinue(c rune) bool {
	return isIDStart(c) || unicode.Is(unicode.Mn, c) || unicode.Is(unicode.Mc, c) || unicode.Is(unicode.Nd, c) ||
		unicode.Is(unicode.Pc, c) || unicode.Is(unicode.Other_ID_Continue, c) || c == 0x200C || c == 0x200D
}

// parseGroupName reads an identifier followed by '>'.
func (p *reParser) parseGroupName() (string, error) {
	var out []rune
	first := true
	for {
		c := p.cur()
		if c == -1 {
			return "", errRegexp
		}
		if c == '>' {
			p.pos++
			break
		}
		if c == '\\' {
			if p.at(1) != 'u' {
				return "", errRegexp
			}
			p.pos += 2
			v, ok := p.readUnicodeEscapeInName()
			if !ok {
				return "", errRegexp
			}
			c = v
		} else {
			p.pos++
			if c >= 0xD800 && c <= 0xDBFF {
				if lo := p.cur(); lo >= 0xDC00 && lo <= 0xDFFF {
					p.pos++
					c = 0x10000 + (c-0xD800)<<10 + (lo - 0xDC00)
				}
			}
		}
		if first {
			if !isIDStart(c) {
				return "", errRegexp
			}
		} else if !isIDContinue(c) {
			return "", errRegexp
		}
		first = false
		out = append(out, c)
	}
	if len(out) == 0 {
		return "", errRegexp
	}
	return string(out), nil
}

func (p *reParser) readUnicodeEscapeInName() (rune, bool) {
	if p.cur() == '{' {
		p.pos++
		var v rune
		n := 0
		for isHex(p.cur()) {
			v = v*16 + hexVal(p.cur())
			if v > 0x10FFFF {
				return 0, false
			}
			p.pos++
			n++
		}
		if n == 0 || p.cur() != '}' {
			return 0, false
		}
		p.pos++
		return v, true
	}
	var v rune
	for i := 0; i < 4; i++ {
		if !isHex(p.cur()) {
			return 0, false
		}
		v = v*16 + hexVal(p.cur())
		p.pos++
	}
	if v >= 0xD800 && v <= 0xDBFF && p.cur() == '\\' && p.at(1) == 'u' {
		save := p.pos
		p.pos += 2
		var lo rune
		ok := true
		for i := 0; i < 4; i++ {
			if !isHex(p.cur()) {
				ok = false
				break
			}
			lo = lo*16 + hexVal(p.cur())
			p.pos++
		}
		if ok && lo >= 0xDC00 && lo <= 0xDFFF {
			return 0x10000 + (v-0xD800)<<10 + (lo - 0xDC00), true
		}
		p.pos = save
	}
	return v, true
}

func singleChar(c rune) *reNode {
	return &reNode{kind: reChar, set: &charSet{items: []classItem{{lo: c, hi: c}}}}
}

// parseAtomEscape parses an escape outside a class; p.pos is at the backslash.
func (p *reParser) parseAtomEscape() (*reNode, error) {
	c := p.at(1)
	switch c {
	case 'd', 'D', 's', 'S', 'w', 'W':
		p.pos += 2
		return &reNode{kind: reChar, set: &charSet{items: []classItem{{esc: c}}}}, nil
	case 'k':
		if p.hasNamed {
			p.pos += 2
			if p.cur() != '<' {
				return nil, errRegexp
			}
			p.pos++
			nm, err := p.parseGroupName()
			if err != nil {
				return nil, err
			}
			n := &reNode{kind: reBackref, refNm: nm}
			p.namedRefs = append(p.namedRefs, n)
			return n, nil
		}
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		save := p.pos
		p.pos++
		n := 0
		for isDigit(p.cur()) {
			if n < 1<<20 {
				n = n*10 + int(p.cur()-'0')
			}
			p.pos++
		}
		if n <= p.totalCaps {
			return &reNode{kind: reBackref, refs: []int{n}}, nil
		}
		p.pos = save
		if c == '8' || c == '9' {
			p.pos += 2
			return singleChar(c), nil
		}
		p.pos++
		return singleChar(p.parseOctal()), nil
	case '0':
		p.pos++
		return singleChar(p.parseOctal()), nil
	}
	v := p.parseCharacterEscape(false)
	return singleChar(v), nil
}

// parseOctal follows V8's ParseOctalLiteral; p.pos is at the first digit.
func (p *reParser) parseOctal() rune {
	v := p.cur() - '0'
	p.pos++
	if c := p.cur(); c >= '0' && c <= '7' {
		v = v*8 + c - '0'
		p.pos++
		if c2 := p.cur(); v < 32 && c2 >= '0' && c2 <= '7' {
			v = v*8 + c2 - '0'
			p.pos++
		}
	}
	return v
}

// parseCharacterEscape follows V8's ParseCharacterEscape for non-unicode mode; p.pos is at the backslash.
func (p *reParser) parseCharacterEscape(inClass bool) rune {
	p.pos++ // backslash
	c := p.cur()
	switch c {
	case 'f':
		p.pos++
		return '\f'
	case 'n':
		p.pos++
		return '\n'
	case 'r':
		p.pos++
		return '\r'
	case 't':
		p.pos++
		return '\t'
	case 'v':
		p.pos++
		return '\v'
	case 'c':
		ctl := p.at(1)
		letter := ctl &^ ('a' ^ 'A')
		if ctl >= 0 && letter >= 'A' && letter <= 'Z' {
			p.pos += 2
			return ctl & 0x1F
		}
		if inClass && (isDigit(ctl) || ctl == '_') {
			p.pos += 2
			return ctl & 0x1F
		}
		return '\\'
	case '0':
		if !isDigit(p.at(1)) {
			p.pos++
			return 0
		}
		return p.parseOctal()
	case '1', '2', '3', '4', '5', '6', '7':
		return p.parseOctal()
	case 'x':
		p.pos++
		if isHex(p.cur()) && isHex(p.at(1)) {
			v := hexVal(p.cur())*16 + hexVal(p.at(1))
			p.pos += 2
			return v
		}
		return 'x'
	case 'u':
		p.pos++
		for i := 0; i < 4; i++ {
			if !isHex(p.at(i)) {
				return 'u'
			}
		}
		v := hexVal(p.cur())<<12 | hexVal(p.at(1))<<8 | hexVal(p.at(2))<<4 | hexVal(p.at(3))
		p.pos += 4
		return v
	}
	p.pos++
	return c
}

// parseClass parses [...]; p.pos is at '['.
func (p *reParser) parseClass() (*charSet, error) {
	p.pos++
	cs := &charSet{}
	if p.cur() == '^' {
		cs.negate = true
		p.pos++
	}
	for p.pos < len(p.src) && p.cur() != ']' {
		a, aEsc, err := p.parseClassAtom()
		if err != nil {
			return nil, err
		}
		if p.cur() == '-' {
			p.pos++
			if p.cur() == -1 {
				return nil, errRegexp
			}
			if p.cur() == ']' {
				cs.add(a, aEsc)
				cs.add('-', 0)
				break
			}
			b, bEsc, err := p.parseClassAtom()
			if err != nil {
				return nil, err
			}
			if aEsc != 0 || bEsc != 0 {
				cs.add(a, aEsc)
				cs.add('-', 0)
				cs.add(b, bEsc)
				continue
			}
			if a > b {
				return nil, errRegexp
			}
			cs.items = append(cs.items, classItem{lo: a, hi: b})
			continue
		}
		cs.add(a, aEsc)
	}
	if p.cur() != ']' {
		return nil, errRegexp
	}
	p.pos++
	return cs, nil
}

func (cs *charSet) add(c rune, esc rune) {
	if esc != 0 {
		cs.items = append(cs.items, classItem{esc: esc})
		return
	}
	cs.items = append(cs.items, classItem{lo: c, hi: c})
}

func (p *reParser) parseClassAtom() (rune, rune, error) {
	c := p.cur()
	if c != '\\' {
		p.pos++
		return c, 0, nil
	}
	switch p.at(1) {
	case -1:
		return 0, 0, errRegexp
	case 'b':
		p.pos += 2
		return 8, 0, nil
	case 'd', 'D', 's', 'S', 'w', 'W':
		e := p.at(1)
		p.pos += 2
		return 0, e, nil
	}
	return p.parseCharacterEscape(true), 0, nil
}

// compileNode turns the syntax tree into continuation-passing matchers; back selects the lookbehind direction.
func compileNode(n *reNode, back bool) mfunc {
	switch n.kind {
	case reSeq:
		if len(n.subs) == 0 {
			return func(s *mstate, i int, k cont) bool { return k(i) }
		}
		ms := make([]mfunc, len(n.subs))
		for i, sub := range n.subs {
			ms[i] = compileNode(sub, back)
		}
		if back {
			for i, j := 0, len(ms)-1; i < j; i, j = i+1, j-1 {
				ms[i], ms[j] = ms[j], ms[i]
			}
		}
		m := ms[len(ms)-1]
		for i := len(ms) - 2; i >= 0; i-- {
			a, b := ms[i], m
			m = func(s *mstate, i int, k cont) bool {
				return a(s, i, func(j int) bool { return b(s, j, k) })
			}
		}
		return m
	case reAlt:
		if len(n.subs) == 1 {
			return compileNode(n.subs[0], back)
		}
		ms := make([]mfunc, len(n.subs))
		for i, sub := range n.subs {
			ms[i] = compileNode(sub, back)
		}
		return func(s *mstate, i int, k cont) bool {
			for _, m := range ms {
				if m(s, i, k) {
					return true
				}
			}
			return false
		}
	case reChar:
		set := n.set
		if back {
			return func(s *mstate, i int, k cont) bool {
				if i > 0 && set.match(s.in[i-1], s.icase) {
					return k(i - 1)
				}
				return false
			}
		}
		return func(s *mstate, i int, k cont) bool {
			if i < len(s.in) && set.match(s.in[i], s.icase) {
				return k(i + 1)
			}
			return false
		}
	case reDot:
		if back {
			return func(s *mstate, i int, k cont) bool {
				if i > 0 && !isLineTerminator(s.in[i-1]) {
					return k(i - 1)
				}
				return false
			}
		}
		return func(s *mstate, i int, k cont) bool {
			if i < len(s.in) && !isLineTerminator(s.in[i]) {
				return k(i + 1)
			}
			return false
		}
	case reGroup:
		body := compileNode(n.subs[0], back)
		if n.capIdx == 0 {
			return body
		}
		idx := n.capIdx
		return func(s *mstate, i int, k cont) bool {
			return body(s, i, func(j int) bool {
				o0, o1 := s.caps[2*idx], s.caps[2*idx+1]
				if back {
					s.caps[2*idx], s.caps[2*idx+1] = j, i
				} else {
					s.caps[2*idx], s.caps[2*idx+1] = i, j
				}
				if k(j) {
					return true
				}
				s.caps[2*idx], s.caps[2*idx+1] = o0, o1
				return false
			})
		}
	case reLook:
		body := compileNode(n.subs[0], n.behind)
		neg := n.neg
		return func(s *mstate, i int, k cont) bool {
			saved := append([]int(nil), s.caps...)
			ok := body(s, i, func(int) bool { return true })
			if neg {
				copy(s.caps, saved)
				if ok {
					return false
				}
				return k(i)
			}
			if !ok {
				copy(s.caps, saved)
				return false
			}
			if k(i) {
				return true
			}
			copy(s.caps, saved)
			return false
		}
	case reBackref:
		node := n
		return func(s *mstate, i int, k cont) bool {
			st, en := -1, -1
			for _, idx := range node.refs {
				if s.caps[2*idx] >= 0 {
					st, en = s.caps[2*idx], s.caps[2*idx+1]
					break
				}
			}
			if st < 0 {
				return k(i)
			}
			ln := en - st
			from := i
			if back {
				from = i - ln
			}
			if from < 0 || from+ln > len(s.in) {
				return false
			}
			for x := 0; x < ln; x++ {
				a, b := s.in[st+x], s.in[from+x]
				if a != b && (!s.icase || canonicalize(a) != canonicalize(b)) {
					return false
				}
			}
			if back {
				return k(i - ln)
			}
			return k(i + ln)
		}
	case reAssert:
		switch n.assert {
		case '^':
			return func(s *mstate, i int, k cont) bool { return i == 0 && k(i) }
		case '$':
			return func(s *mstate, i int, k cont) bool { return i == len(s.in) && k(i) }
		default:
			want := n.assert == 'b'
			return func(s *mstate, i int, k cont) bool {
				a := i > 0 && isWordUnit(s.in[i-1])
				b := i < len(s.in) && isWordUnit(s.in[i])
				return (a != b) == want && k(i)
			}
		}
	case reQuant:
		body := compileNode(n.subs[0], back)
		lo, hi := n.capLo, n.capHi
		greedy := n.greedy
		var rep func(s *mstate, i, min, max int, k cont) bool
		rep = func(s *mstate, i, min, max int, k cont) bool {
			if max == 0 {
				return k(i)
			}
			d := func(j int) bool {
				if min == 0 && j == i {
					return false
				}
				nmin, nmax := min, max
				if nmin > 0 {
					nmin--
				}
				if nmax != reInf {
					nmax--
				}
				return rep(s, j, nmin, nmax, k)
			}
			var saved []int
			if hi > lo {
				saved = append([]int(nil), s.caps[2*lo:2*hi]...)
			}
			clearCaps := func() {
				for x := 2 * lo; x < 2*hi; x++ {
					s.caps[x] = -1
				}
			}
			restoreCaps := func() {
				if saved != nil {
					copy(s.caps[2*lo:2*hi], saved)
				}
			}
			if min > 0 {
				clearCaps()
				if body(s, i, d) {
					return true
				}
				restoreCaps()
				return false
			}
			if greedy {
				clearCaps()
				if body(s, i, d) {
					return true
				}
				restoreCaps()
				return k(i)
			}
			if k(i) {
				return true
			}
			clearCaps()
			if body(s, i, d) {
				return true
			}
			restoreCaps()
			return false
		}
		min, max := n.min, n.max
		return func(s *mstate, i int, k cont) bool { return rep(s, i, min, max, k) }
	}
	return func(s *mstate, i int, k cont) bool { return false }
}
