package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// The source map code needs JSON.parse and JSON.stringify as JavaScript runs them: the top-level object keeps
// its key order and strings are escaped the way JSON.stringify escapes them. Nested objects are plain Go maps,
// so their keys are written in JavaScript's order for a fresh object (integer keys ascending, then the rest
// sorted).

// errNotObject reports a JSON text whose value is not an object.
var errNotObject = errors.New("JSON value is not an object")

// parseJSONObject follows JSON.parse for a text whose value must be an object; it returns the object and its key
// order. A text holding null yields a nil map and no error.
func parseJSONObject(text string) (map[string]any, []string, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	var m map[string]any
	var keys []string
	switch tok {
	case nil:
	case json.Delim('{'):
		m = map[string]any{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, nil, err
			}
			key, ok := kt.(string)
			if !ok {
				return nil, nil, fmt.Errorf("unexpected token %v", kt)
			}
			var v any
			if err := dec.Decode(&v); err != nil {
				return nil, nil, err
			}
			if _, dup := m[key]; !dup {
				keys = append(keys, key)
			}
			m[key] = v
		}
		if _, err := dec.Token(); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, errNotObject
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, errors.New("unexpected non-whitespace character after JSON")
	}
	return m, keys, nil
}

// stringifyObject follows JSON.stringify(obj) for a top-level object written in the given key order.
func stringifyObject(m map[string]any, keys []string) string {
	var b strings.Builder
	writeObject(&b, m, jsKeyOrder(keys))
	return b.String()
}

// jsKeyOrder moves array-index keys to the front in ascending order, as JavaScript orders own properties.
func jsKeyOrder(keys []string) []string {
	var idx, rest []string
	for _, k := range keys {
		if isArrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	slices.SortFunc(idx, func(a, b string) int {
		x, _ := strconv.ParseUint(a, 10, 32)
		y, _ := strconv.ParseUint(b, 10, 32)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	return append(idx, rest...)
}

// isArrayIndex reports whether k is a canonical array index ("0", "1", ... below 2^32-1).
func isArrayIndex(k string) bool {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 64)
	return err == nil && n < math.MaxUint32
}

func writeObject(b *strings.Builder, m map[string]any, keys []string) {
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		v, ok := m[k]
		if !ok || !jsonSerializable(v) {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		writeString(b, k)
		b.WriteByte(':')
		writeValue(b, v)
	}
	b.WriteByte('}')
}

// jsonSerializable reports whether JSON.stringify writes a property holding v (functions are skipped).
func jsonSerializable(v any) bool {
	return !isFunction(v)
}

func writeValue(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case string:
		writeString(b, t)
	case []uint16:
		writeUnits(b, t)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		writeObject(b, t, jsKeyOrder(keys))
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if !jsonSerializable(e) {
				b.WriteString("null")
				continue
			}
			writeValue(b, e)
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, e)
		}
		b.WriteByte(']')
	default:
		if n, ok := toNumber(v); ok {
			if math.IsNaN(n) || math.IsInf(n, 0) {
				b.WriteString("null")
				return
			}
			b.WriteString(formatJSNumber(n))
			return
		}
		b.WriteString("null")
	}
}

// writeString writes s as JSON.stringify quotes a string.
func writeString(b *strings.Builder, s string) {
	writeUnits(b, utf16.Encode([]rune(s)))
}

// writeUnits quotes a UTF-16 string: control characters, quotes and backslashes are escaped, and lone surrogates
// are written as \uXXXX (well-formed JSON.stringify).
func writeUnits(b *strings.Builder, units []uint16) {
	b.WriteByte('"')
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u == '"':
			b.WriteString(`\"`)
		case u == '\\':
			b.WriteString(`\\`)
		case u == '\b':
			b.WriteString(`\b`)
		case u == '\f':
			b.WriteString(`\f`)
		case u == '\n':
			b.WriteString(`\n`)
		case u == '\r':
			b.WriteString(`\r`)
		case u == '\t':
			b.WriteString(`\t`)
		case u < 0x20:
			fmt.Fprintf(b, `\u%04x`, u)
		case u >= 0xD800 && u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000:
			b.WriteRune(utf16.DecodeRune(rune(u), rune(units[i+1])))
			i++
		case u >= 0xD800 && u < 0xE000:
			fmt.Fprintf(b, `\u%04x`, u)
		default:
			b.WriteRune(rune(u))
		}
	}
	b.WriteByte('"')
}

// formatJSNumber follows Number.prototype.toString() for finite numbers.
func formatJSNumber(n float64) string {
	switch {
	case math.IsNaN(n):
		return "NaN"
	case math.IsInf(n, 1):
		return "Infinity"
	case math.IsInf(n, -1):
		return "-Infinity"
	case n == 0:
		return "0"
	}
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	// Shortest round-trip digits and the decimal exponent: n = 0.d1d2...dk * 10^e.
	sci := strconv.FormatFloat(n, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(sci, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, err := strconv.Atoi(expStr)
	if err != nil {
		return sign + sci
	}
	k := len(digits)
	e := exp + 1
	switch {
	case k <= e && e <= 21:
		return sign + digits + strings.Repeat("0", e-k)
	case 0 < e && e <= 21:
		return sign + digits[:e] + "." + digits[e:]
	case -6 < e && e <= 0:
		return sign + "0." + strings.Repeat("0", -e) + digits
	}
	expSign := "+"
	if e-1 < 0 {
		expSign = "-"
	}
	exponent := strconv.Itoa(abs(e - 1))
	if k == 1 {
		return sign + digits + "e" + expSign + exponent
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + exponent
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
