package cli

import (
	"math"
	"regexp"
	"strings"

	"github.com/gulpjs/gulp/internal/jsnum"
)

// The parsed command line is held as JavaScript values so parsing can follow yargs-parser 20.2.4 exactly:
// undefinedValue, bool, float64, string, []any (arrays from repeated options) and map[string]any (objects
// from dot notation).

// undefinedValue follows JavaScript's undefined stored under a key (a declared option default).
type undefinedValue struct{}

var undefined = undefinedValue{}

// option is one gulp-cli option (lib/shared/options/cli-options.js) as yargs registers it with the parser.
type option struct {
	key         string
	alias       string
	kind        string // "boolean", "string", "number", "count" or "" (untyped)
	requiresArg bool
	hasDefault  bool // `default: undefined` is declared
	tag         string
}

// cliOptions lists the options in declaration order.
var cliOptions = []option{
	{key: "help", alias: "h", kind: "boolean", tag: "FLAG_HELP"},
	{key: "version", alias: "v", kind: "boolean", tag: "FLAG_VERSION"},
	{key: "preload", kind: "string", requiresArg: true, tag: "FLAG_PRELOAD"},
	{key: "gulpfile", alias: "f", kind: "string", requiresArg: true, tag: "FLAG_GULPFILE"},
	{key: "cwd", kind: "string", requiresArg: true, tag: "FLAG_CWD"},
	{key: "tasks", alias: "T", kind: "boolean", tag: "FLAG_TASKS"},
	{key: "tasks-simple", kind: "boolean", tag: "FLAG_TASKS_SIMPLE"},
	{key: "tasks-json", tag: "FLAG_TASKS_JSON"},
	{key: "tasks-depth", alias: "depth", kind: "number", requiresArg: true, hasDefault: true, tag: "FLAG_TASKS_DEPTH"},
	{key: "compact-tasks", kind: "boolean", hasDefault: true, tag: "FLAG_COMPACT_TASKS"},
	{key: "sort-tasks", kind: "boolean", hasDefault: true, tag: "FLAG_SORT_TASKS"},
	{key: "color", kind: "boolean", tag: "FLAG_COLOR"},
	{key: "no-color", kind: "boolean", tag: "FLAG_NO_COLOR"},
	{key: "silent", alias: "S", kind: "boolean", hasDefault: true, tag: "FLAG_SILENT"},
	{key: "continue", kind: "boolean", hasDefault: true, tag: "FLAG_CONTINUE"},
	{key: "series", kind: "boolean", hasDefault: true, tag: "FLAG_SERIES"},
	{key: "log-level", alias: "L", kind: "count", hasDefault: true, tag: "FLAG_LOG_LEVEL"},
}

// argvParser is one yargs-parser run with gulp-cli's option configuration.
type argvParser struct {
	aliases    map[string][]string
	bools      map[string]bool
	strs       map[string]bool
	numbers    map[string]bool
	counts     map[string]bool
	nargs      map[string]bool
	defaults   map[string]any
	newAliases map[string]bool
	argv       map[string]any
	positional []any
	err        string
}

var (
	negativeRe       = regexp.MustCompile(`^-([0-9]+(\.[0-9]+)?|\.[0-9]+)$`)
	negatedBooleanRe = regexp.MustCompile(`^--no-(.+)`)
	longEqRe         = regexp.MustCompile(`^--.+=`)
	longEqSplitRe    = regexp.MustCompile(`^--?([^=]+)=([\s\S]*)$`)
	longRe           = regexp.MustCompile(`^--.+`)
	longKeyRe        = regexp.MustCompile(`^--?(.+)`)
	dotEqRe          = regexp.MustCompile(`^-.\..+=`)
	dotEqSplitRe     = regexp.MustCompile(`^-([^=]+)=([\s\S]*)$`)
	dotRe            = regexp.MustCompile(`^-.\..+`)
	dotKeyRe         = regexp.MustCompile(`^-(.\..+)`)
	shortRe          = regexp.MustCompile(`^-[^-]+`)
	trueFalseRe      = regexp.MustCompile(`^(true|false)$`)
	alphaRe          = regexp.MustCompile(`[A-Za-z]`)
	shortNumberRe    = regexp.MustCompile(`^-?\d+(\.\d*)?(e-?\d+)?$`)
	nonWordRe        = regexp.MustCompile(`\W`)
	upperRe          = regexp.MustCompile(`[A-Z]`)
	flagLikeRe       = regexp.MustCompile(`^(-|--)[^-]`)
	dashNonDigitRe   = regexp.MustCompile(`^-[^0-9]`)
	hexRe            = regexp.MustCompile(`(?i)^0x[0-9a-f]+$`)
	decimalRe        = regexp.MustCompile(`^[-]?(?:\d+(?:\.\d*)?|\.\d+)(e[-+]?\d+)?$`)
)

// parseArgv parses args as gulp-cli's yargs 16 instance does, including yargs' post-processing (arguments
// after "--" become positionals, numeric positionals become numbers). The error is yargs-parser's message.
func parseArgv(args []string) (map[string]any, string) {
	p := &argvParser{
		aliases:    map[string][]string{},
		bools:      map[string]bool{},
		strs:       map[string]bool{},
		numbers:    map[string]bool{},
		counts:     map[string]bool{},
		nargs:      map[string]bool{},
		defaults:   map[string]any{},
		newAliases: map[string]bool{},
		argv:       map[string]any{},
	}
	declared := map[string][]string{}
	for _, o := range cliOptions {
		switch o.kind {
		case "boolean":
			p.bools[o.key] = true
		case "string":
			p.strs[o.key] = true
		case "number":
			p.numbers[o.key] = true
		case "count":
			p.counts[o.key] = true
		}
		if o.requiresArg {
			p.nargs[o.key] = true
		}
		if o.hasDefault {
			p.defaults[o.key] = undefined
		}
		if o.alias != "" {
			declared[o.key] = []string{o.alias}
		}
	}
	p.extendAliases(declared)
	for key, def := range p.defaults {
		for _, a := range p.aliases[key] {
			p.defaults[a] = def
		}
	}
	notFlags := p.parseLoop(args)
	p.applyDefaultsAndAliases()
	for key := range p.counts {
		if !p.hasKey(p.argv, strings.Split(key, ".")) {
			p.setArg(key, 0.0)
		}
	}
	argv := p.argv
	positional := append(p.positional, toAny(notFlags)...)
	for i, a := range positional {
		if looksLikeNumber(a) && jsnum.IsSafeInteger(math.Floor(jsnum.ParseFloat(jsString(a)))) {
			positional[i] = jsNumberOf(a)
		}
	}
	argv["_"] = positional
	return argv, p.err
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// extendAliases follows yargs-parser's alias expansion (camel-case variants and the reverse lookups).
func (p *argvParser) extendAliases(declared map[string][]string) {
	keys := make([]string, 0, len(cliOptions))
	for _, o := range cliOptions {
		keys = append(keys, o.key)
	}
	for _, key := range keys {
		if _, ok := p.aliases[key]; ok {
			continue
		}
		list := append([]string(nil), declared[key]...)
		for _, x := range append(append([]string(nil), list...), key) {
			if strings.Contains(x, "-") {
				c := camelCase(x)
				if c != key && !contains(list, c) {
					list = append(list, c)
					p.newAliases[c] = true
				}
			}
		}
		for _, x := range append(append([]string(nil), list...), key) {
			if len(x) > 1 && upperRe.MatchString(x) {
				c := decamelize(x)
				if c != key && !contains(list, c) {
					list = append(list, c)
					p.newAliases[c] = true
				}
			}
		}
		p.aliases[key] = list
		for _, x := range list {
			others := []string{key}
			for _, y := range list {
				if y != x {
					others = append(others, y)
				}
			}
			p.aliases[x] = others
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// is reports whether key or one of its aliases is in the flag set (checkAllAliases).
func (p *argvParser) is(key string, set map[string]bool) bool {
	if set[key] {
		return true
	}
	for _, a := range p.aliases[key] {
		if set[a] {
			return true
		}
	}
	return false
}

// parseLoop walks the arguments; it returns the arguments after "--".
func (p *argvParser) parseLoop(args []string) []string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case longEqRe.MatchString(arg):
			if m := longEqSplitRe.FindStringSubmatch(arg); m != nil {
				if p.is(m[1], p.nargs) {
					i = p.eatNargs(i, m[1], args, &m[2])
				} else {
					p.setArg(m[1], m[2])
				}
			}
		case negatedBooleanRe.MatchString(arg):
			m := negatedBooleanRe.FindStringSubmatch(arg)
			p.setArg(m[1], false)
		case longRe.MatchString(arg):
			key := longKeyRe.FindStringSubmatch(arg)[1]
			if p.is(key, p.nargs) {
				i = p.eatNargs(i, key, args, nil)
				break
			}
			i = p.valueAfter(i, key, args, func(next string) bool {
				return !strings.HasPrefix(next, "-") || negativeRe.MatchString(next)
			})
		case dotEqRe.MatchString(arg):
			if m := dotEqSplitRe.FindStringSubmatch(arg); m != nil {
				p.setArg(m[1], m[2])
			}
		case dotRe.MatchString(arg) && !negativeRe.MatchString(arg):
			key := dotKeyRe.FindStringSubmatch(arg)[1]
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !p.is(key, p.bools) && !p.is(key, p.counts) {
				p.setArg(key, args[i+1])
				i++
			} else {
				p.setArg(key, p.defaultValue(key))
			}
		case shortRe.MatchString(arg) && !negativeRe.MatchString(arg):
			i = p.shortGroup(i, arg, args)
		case arg == "--":
			return args[i+1:]
		default:
			p.positional = append(p.positional, arg)
		}
	}
	return nil
}

// valueAfter handles an option that may take the next argument as its value.
func (p *argvParser) valueAfter(i int, key string, args []string, takes func(string) bool) int {
	if i+1 < len(args) {
		next := args[i+1]
		if takes(next) && !p.is(key, p.bools) && !p.is(key, p.counts) {
			p.setArg(key, next)
			return i + 1
		}
		if trueFalseRe.MatchString(next) {
			p.setArg(key, next)
			return i + 1
		}
	}
	p.setArg(key, p.defaultValue(key))
	return i
}

// shortGroup parses a short option group such as -LLL or -fgulpfile.js.
func (p *argvParser) shortGroup(i int, arg string, args []string) int {
	runes := []rune(arg)
	letters := runes[1 : len(runes)-1]
	broken := false
	for j := 0; j < len(letters); j++ {
		next := string(runes[j+2:])
		letter := string(letters[j])
		if j+1 < len(letters) && letters[j+1] == '=' {
			value := string(runes[j+3:])
			if p.is(letter, p.nargs) {
				i = p.eatNargs(i, letter, args, &value)
			} else {
				p.setArg(letter, value)
			}
			broken = true
			break
		}
		if next == "-" {
			p.setArg(letter, next)
			continue
		}
		if alphaRe.MatchString(letter) && shortNumberRe.MatchString(next) && !p.is(next, p.bools) {
			p.setArg(letter, next)
			broken = true
			break
		}
		if j+1 < len(letters) && nonWordRe.MatchString(string(letters[j+1])) {
			p.setArg(letter, next)
			broken = true
			break
		}
		p.setArg(letter, p.defaultValue(letter))
	}
	key := string(runes[len(runes)-1])
	if broken || key == "-" {
		return i
	}
	if p.is(key, p.nargs) {
		return p.eatNargs(i, key, args, nil)
	}
	return p.valueAfter(i, key, args, func(next string) bool {
		return !flagLikeRe.MatchString(next) || negativeRe.MatchString(next)
	})
}

// eatNargs consumes the single argument a requiresArg option needs (nargs NaN counts as 1).
func (p *argvParser) eatNargs(i int, key string, args []string, afterEq *string) int {
	available := 0
	if afterEq != nil {
		available = 1
	}
	for ii := i + 1; ii < len(args); ii++ {
		if !dashNonDigitRe.MatchString(args[ii]) || negativeRe.MatchString(args[ii]) {
			available++
		} else {
			break
		}
	}
	if available < 1 {
		p.err = "Not enough arguments following: " + key
	}
	consumed := available
	if consumed > 1 {
		consumed = 1
	}
	if afterEq != nil && consumed > 0 {
		p.setArg(key, *afterEq)
		consumed--
	}
	for ii := i + 1; ii < consumed+i+1; ii++ {
		p.setArg(key, args[ii])
	}
	return i + consumed
}

// defaultValue is the value an option given without one takes.
func (p *argvParser) defaultValue(key string) any {
	if !p.is(key, p.bools) && !p.is(key, p.counts) {
		if v, ok := p.defaults[key]; ok {
			return v
		}
	}
	switch {
	case p.is(key, p.strs):
		return ""
	case p.is(key, p.numbers):
		return undefined
	}
	return true
}

// setArg stores a value under key and its aliases.
func (p *argvParser) setArg(key string, val any) {
	if strings.Contains(key, "-") {
		parts := strings.Split(key, ".")
		for i, part := range parts {
			parts[i] = camelCase(part)
		}
		p.addNewAlias(key, strings.Join(parts, "."))
	}
	value := p.processValue(key, val)
	split := strings.Split(key, ".")
	p.setKey(p.argv, split, value)
	for _, x := range p.aliases[key] {
		p.setKey(p.argv, strings.Split(x, "."), value)
	}
	if len(split) > 1 {
		full := p.aliases[key]
		for _, x := range p.aliases[split[0]] {
			props := append(strings.Split(x, "."), split[1:]...)
			if !contains(full, strings.Join(props, ".")) {
				p.setKey(p.argv, props, value)
			}
		}
	}
}

func (p *argvParser) addNewAlias(key, alias string) {
	if len(p.aliases[key]) == 0 {
		p.aliases[key] = []string{alias}
		p.newAliases[alias] = true
	}
	if len(p.aliases[alias]) == 0 {
		p.addNewAlias(alias, key)
	}
}

// incrementMarker is the value increment() returns for a count, which setKey recognises (it is the number 1).
const incrementMarker = 1.0

func (p *argvParser) processValue(key string, val any) any {
	if s, ok := val.(string); ok && len(s) > 1 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		val = s[1 : len(s)-1]
	}
	if p.is(key, p.bools) || p.is(key, p.counts) {
		if s, ok := val.(string); ok {
			val = s == "true"
		}
	}
	value := p.maybeCoerceNumber(key, val)
	if p.is(key, p.counts) {
		switch value.(type) {
		case undefinedValue, bool:
			value = incrementMarker
		}
	}
	return value
}

func (p *argvParser) maybeCoerceNumber(key string, value any) any {
	if p.is(key, p.strs) || p.is(key, p.bools) {
		return value
	}
	if _, isArray := value.([]any); isArray {
		return value
	}
	coerce := looksLikeNumber(value) && jsnum.IsSafeInteger(math.Floor(jsnum.ParseFloat(jsString(value))))
	if _, isUndef := value.(undefinedValue); coerce || (!isUndef && p.is(key, p.numbers)) {
		return jsNumberOf(value)
	}
	return value
}

func (p *argvParser) hasKey(obj map[string]any, keys []string) bool {
	o := any(obj)
	for _, k := range keys[:len(keys)-1] {
		m, ok := o.(map[string]any)
		if !ok {
			return false
		}
		next, present := m[k]
		if !present || !truthyValue(next) {
			o = map[string]any{}
			continue
		}
		o = next
	}
	m, ok := o.(map[string]any)
	if !ok {
		return false
	}
	_, present := m[keys[len(keys)-1]]
	return present
}

func sanitizeKey(key string) string {
	if key == "__proto__" {
		return "___proto___"
	}
	return key
}

// setKey follows yargs-parser's setKey: dot paths create objects, repeated values become arrays, counts increment.
func (p *argvParser) setKey(obj map[string]any, keys []string, value any) {
	o := obj
	for _, k := range keys[:len(keys)-1] {
		k = sanitizeKey(k)
		cur, present := o[k]
		if !present || cur == any(undefined) {
			cur = map[string]any{}
			o[k] = cur
		}
		switch c := cur.(type) {
		case map[string]any:
			o = c
		case []any:
			next := map[string]any{}
			o[k] = append(c, next)
			o = next
		default:
			next := map[string]any{}
			o[k] = []any{c, next}
			o = next
		}
	}
	key := sanitizeKey(keys[len(keys)-1])
	cur, present := o[key]
	isUndef := !present || cur == any(undefined)
	if f, ok := value.(float64); ok && f == incrementMarker {
		if isUndef {
			o[key] = 1.0
		} else {
			o[key] = jsAddOne(cur)
		}
		return
	}
	if arr, ok := cur.([]any); ok {
		o[key] = append(arr, value)
		return
	}
	if !isUndef && !p.is(key, p.counts) && !p.is(key, p.bools) {
		o[key] = []any{cur, value}
		return
	}
	o[key] = value
}

// applyDefaultsAndAliases stores the declared defaults (undefined) for keys the command line did not set.
func (p *argvParser) applyDefaultsAndAliases() {
	for _, o := range cliOptions {
		if !o.hasDefault {
			continue
		}
		key := o.key
		if p.hasKey(p.argv, strings.Split(key, ".")) {
			continue
		}
		p.setKey(p.argv, strings.Split(key, "."), undefined)
		for _, x := range p.aliases[key] {
			if p.hasKey(p.argv, strings.Split(x, ".")) {
				continue
			}
			p.setKey(p.argv, strings.Split(x, "."), undefined)
		}
	}
}

// camelCase follows yargs-parser's camelCase.
func camelCase(str string) string {
	str = strings.ToLower(str)
	if !strings.ContainsAny(str, "-_") {
		return str
	}
	var b strings.Builder
	upper := false
	start := len(str) - len(strings.TrimLeft(str, "-"))
	for i, r := range str {
		if i < start {
			continue
		}
		if upper {
			upper = false
			r = []rune(strings.ToUpper(string(r)))[0]
		}
		if i != 0 && (r == '-' || r == '_') {
			upper = true
			continue
		}
		if r != '-' && r != '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// decamelize follows yargs-parser's decamelize with "-".
func decamelize(str string) string {
	var b strings.Builder
	for i, r := range []rune(str) {
		lower := []rune(strings.ToLower(string(r)))[0]
		if lower != r && i > 0 {
			b.WriteByte('-')
			b.WriteRune(lower)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// looksLikeNumber follows yargs-parser's looksLikeNumber.
func looksLikeNumber(x any) bool {
	switch v := x.(type) {
	case nil, undefinedValue:
		return false
	case float64:
		return true
	case string:
		if hexRe.MatchString(v) {
			return true
		}
		if len(v) > 1 && v[0] == '0' {
			return false
		}
		return decimalRe.MatchString(v)
	}
	s := jsString(x)
	return hexRe.MatchString(s) || decimalRe.MatchString(s)
}
