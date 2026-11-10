// Package jsnum formats and parses numbers the way JavaScript does, for the places where gulp turns a
// number into text it prints (task names coerced from the command line, JSON values).
package jsnum

import (
	"math"
	"strconv"
	"strings"
)

// Format renders f as JavaScript's Number.prototype.toString does: the shortest round-trip digits, in plain notation for magnitudes in
// [1e-7, 1e21) and in exponent notation ("1e+21", "1.5e-7") outside it.
func Format(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	// digits and exponent of the shortest representation: d.ddd x 10^exp
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expText, _ := strings.Cut(e, "e")
	exp, err := strconv.Atoi(expText)
	if err != nil {
		return sign + e
	}
	digits := strings.Replace(mant, ".", "", 1)
	k := len(digits)
	n := exp + 1 // position of the decimal point relative to the digits
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	expSign := "+"
	if n-1 < 0 {
		expSign = "-"
	}
	expAbs := strconv.Itoa(absInt(n - 1))
	if k == 1 {
		return sign + digits + "e" + expSign + expAbs
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + expAbs
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Number is the Number(string) conversion: surrounding whitespace is ignored, "" is 0, "0x"/"0o"/"0b" prefixes
// are integers in that base, "Infinity" is accepted with a sign, and anything else that is not a decimal
// literal is NaN.
func Number(s string) float64 {
	s = strings.TrimFunc(s, isJSSpace)
	if s == "" {
		return 0
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			return parseBase(s[2:], base)
		}
	}
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 {
		return math.NaN()
	}
	if body == "Infinity" {
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	if !isDecimalLiteral(body) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// out of range values parse to the infinities ParseFloat already returns with ErrRange
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

// parseBase converts the digits of a prefixed integer literal, NaN for an invalid digit.
func parseBase(digits string, base int) float64 {
	if digits == "" {
		return math.NaN()
	}
	var v float64
	for _, c := range digits {
		d := digitValue(c)
		if d < 0 || d >= base {
			return math.NaN()
		}
		v = v*float64(base) + float64(d)
	}
	return v
}

func digitValue(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

// isDecimalLiteral reports whether s is digits with an optional fraction and exponent ("1", "1.", ".5", "1e3").
func isDecimalLiteral(s string) bool {
	i, n := 0, len(s)
	intDigits := 0
	for i < n && s[i] >= '0' && s[i] <= '9' {
		i++
		intDigits++
	}
	fracDigits := 0
	if i < n && s[i] == '.' {
		i++
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i++
			fracDigits++
		}
	}
	if intDigits+fracDigits == 0 {
		return false
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < n && (s[i] == '+' || s[i] == '-') {
			i++
		}
		expDigits := 0
		for i < n && s[i] >= '0' && s[i] <= '9' {
			i++
			expDigits++
		}
		if expDigits == 0 {
			return false
		}
	}
	return i == n
}

// ParseFloat is the global parseFloat: the longest decimal prefix after leading whitespace, NaN when there is
// none.
func ParseFloat(s string) float64 {
	s = strings.TrimLeftFunc(s, isJSSpace)
	end := 0
	if end < len(s) && (s[end] == '+' || s[end] == '-') {
		end++
	}
	if strings.HasPrefix(s[end:], "Infinity") {
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	best := -1
	for i := end + 1; i <= len(s); i++ {
		if isDecimalLiteral(s[end:i]) {
			best = i
		}
	}
	if best < 0 {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s[:best], 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

// ParseInt is parseInt(s, 10): optional sign and the longest run of decimal digits after leading whitespace,
// NaN when there are no digits.
func ParseInt(s string) float64 {
	s = strings.TrimLeftFunc(s, isJSSpace)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return math.Inf(1)
	}
	if neg {
		return -f
	}
	return f
}

// IsSafeInteger reports whether f is an integer whose magnitude is at most 2^53 - 1 (Number.isSafeInteger).
func IsSafeInteger(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) && math.Abs(f) <= 1<<53-1
}

// isJSSpace reports whether r is JavaScript white space or a line terminator.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}
