package cli

import (
	"math"
	"strings"

	"github.com/gulpjs/gulp/internal/jsnum"
)

// jsString is String(v) for the values the parser and the config files produce.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case undefinedValue:
		return "undefined"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return jsnum.Format(x)
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			switch e.(type) {
			case nil, undefinedValue:
			default:
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	}
	return ""
}

// jsNumberOf is Number(v).
func jsNumberOf(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case undefinedValue:
		return math.NaN()
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		return jsnum.Number(x)
	case []any, map[string]any:
		return jsnum.Number(jsString(x))
	}
	return math.NaN()
}

// truthyValue is JavaScript truthiness.
func truthyValue(v any) bool {
	switch x := v.(type) {
	case nil, undefinedValue:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// isUndefined reports whether v is undefined (a missing key reads as nil-free undefined).
func isUndefined(v any) bool {
	_, ok := v.(undefinedValue)
	return ok
}

// jsAddOne is v + 1 with JavaScript's + operator.
func jsAddOne(v any) any {
	switch x := v.(type) {
	case string:
		return x + "1"
	case []any, map[string]any:
		return jsString(x) + "1"
	}
	return jsNumberOf(v) + 1
}

// jsGreater is v > n for a number n with JavaScript's coercion of v.
func jsGreater(v any, n float64) bool {
	f := jsNumberOf(v)
	return !math.IsNaN(f) && f > n
}

// jsTrim removes leading and trailing whitespace as String.prototype.trim does: the JavaScript WhiteSpace and
// LineTerminator characters, which differ from unicode.IsSpace (U+0085 is kept, U+FEFF is removed).
func jsTrim(s string) string {
	return strings.TrimFunc(s, isJSWhitespace)
}

// isJSWhitespace reports whether r is JavaScript WhiteSpace or a LineTerminator.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}
