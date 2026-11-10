package vfs

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vinyl"
)

// This file resolves options as resolve-options 2.0.0 and value-or-function 4.0.0 do: every option has a list of
// accepted types and a default; a function value is called with the file and its result coerced; a value (or result)
// of the wrong type falls back to the default, which may itself be a function of the file.

// Coercer names of value-or-function.
const (
	typeString  = "string"
	typeNumber  = "number"
	typeBoolean = "boolean"
	typeDate    = "date"
)

// optionDef is one entry of a resolve-options config.
type optionDef struct {
	types []string
	def   any // constant, nil for none, or func(*vinyl.File) any
}

// resolver is createResolver(config, options).
type resolver struct {
	config map[string]optionDef
	opts   Options
}

func newResolver(config map[string]optionDef, opts Options) *resolver {
	return &resolver{config: config, opts: opts}
}

// resolve is resolver.resolve(key, file). Strings come back as string, numbers as float64, booleans as bool and
// dates as float64 milliseconds (NaN for an invalid date); nil is undefined.
func (r *resolver) resolve(key string, f *vinyl.File) any {
	def, ok := r.config[key]
	if !ok {
		return nil
	}
	if v := r.opts[key]; v != nil {
		if out := normalizeValue(def.types, v, f); out != nil {
			return out
		}
	}
	if fn, ok := def.def.(func(*vinyl.File) any); ok {
		return fn(f)
	}
	return def.def
}

// boolean resolves a boolean option.
func (r *resolver) boolean(key string, f *vinyl.File) bool {
	return truthy(r.resolve(key, f))
}

// normalizeValue follows value-or-function's normalize: the coercers are tried in order and a function value is called
// with the file for each of them.
func normalizeValue(types []string, value any, f *vinyl.File) any {
	for _, t := range types {
		val := value
		if out, isFunc := callWithFile(value, f); isFunc {
			val = out
		}
		if out := coerce(t, val); out != nil {
			return out
		}
	}
	return nil
}

// vinylFileType is the reflect type of an option function's parameter.
var vinylFileType = reflect.TypeOf((*vinyl.File)(nil))

// callWithFile calls v when it is a function of one *vinyl.File returning one value. isFunc is false for every
// other value.
func callWithFile(v any, f *vinyl.File) (out any, isFunc bool) {
	if fn, ok := v.(func(*vinyl.File) any); ok && fn != nil {
		return fn(f), true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return nil, false
	}
	t := rv.Type()
	if t.NumIn() != 1 || t.In(0) != vinylFileType || t.NumOut() != 1 {
		return nil, false
	}
	return rv.Call([]reflect.Value{reflect.ValueOf(f)})[0].Interface(), true
}

// isFunction reports whether v is a function value, which the original treats as a per-file option.
func isFunction(v any) bool {
	return v != nil && reflect.ValueOf(v).Kind() == reflect.Func
}

// coerce applies one value-or-function coercer.
func coerce(t string, v any) any {
	switch t {
	case typeString:
		switch s := v.(type) {
		case string:
			return s
		case []string:
			return jsJoin(s)
		}
	case typeBoolean:
		if b, ok := v.(bool); ok {
			return b
		}
	case typeNumber:
		if n, ok := toNumber(v); ok {
			return n
		}
	case typeDate:
		if tm, ok := v.(time.Time); ok {
			return float64(tm.UnixMilli())
		}
		if n, ok := toNumber(v); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return timeClip(n)
		}
	}
	return nil
}

// timeClip is ECMAScript TimeClip: NaN outside the Date range, otherwise truncated toward zero.
func timeClip(ms float64) float64 {
	if math.Abs(ms) > 8.64e15 {
		return math.NaN()
	}
	return math.Trunc(ms) + 0
}

// toNumber converts Go numbers (and a time.Time, whose valueOf is a number in JavaScript) to float64.
func toNumber(v any) (float64, bool) {
	if tm, ok := v.(time.Time); ok {
		return float64(tm.UnixMilli()), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

// truthy follows JavaScript's ToBoolean for the values an option bag holds.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	}
	if n, ok := toNumber(v); ok {
		if _, isTime := v.(time.Time); isTime {
			return true
		}
		return n != 0 && !math.IsNaN(n)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Func, reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface:
		return !rv.IsNil()
	}
	return true
}

// jsString is String(v) for the values an option bag or a glob argument holds.
func jsString(v any) string {
	switch t := v.(type) {
	case nil:
		return "undefined"
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case []string:
		return jsJoin(t)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			if e != nil {
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	}
	if n, ok := toNumber(v); ok {
		if _, isTime := v.(time.Time); !isTime {
			return formatJSNumber(n)
		}
	}
	if isFunction(v) {
		return "function"
	}
	return "[object Object]"
}

// workingDir is the default cwd option of dest and symlink: the process working directory, or "" when it cannot be
// read; resolving the output paths against "" reads the working directory again and returns that error, so the
// file is not written.
func workingDir(*vinyl.File) any {
	wd, err := nodepath.Cwd()
	if err != nil {
		return ""
	}
	return wd
}
