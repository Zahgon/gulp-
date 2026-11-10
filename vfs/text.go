package vfs

import (
	"strings"
	"unicode/utf16"
)

// JavaScript strings are sequences of UTF-16 code units that may hold lone surrogates. The codecs and the
// source map code work on []uint16 so they keep exactly the strings the original sees.

const replacementChar = 0xFFFD

// decodeUTF8 decodes b the way Node's utf8 decoders and TextDecoder do (WHATWG): every maximal invalid
// subsequence becomes one U+FFFD.
func decodeUTF8(b []byte) []uint16 {
	out := make([]uint16, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			out = append(out, uint16(c))
			i++
			continue
		}
		need := 0
		var cp rune
		lower, upper := byte(0x80), byte(0xBF)
		switch {
		case c >= 0xC2 && c <= 0xDF:
			need, cp = 1, rune(c&0x1F)
		case c >= 0xE0 && c <= 0xEF:
			need, cp = 2, rune(c&0x0F)
			if c == 0xE0 {
				lower = 0xA0
			} else if c == 0xED {
				upper = 0x9F
			}
		case c >= 0xF0 && c <= 0xF4:
			need, cp = 3, rune(c&0x07)
			if c == 0xF0 {
				lower = 0x90
			} else if c == 0xF4 {
				upper = 0x8F
			}
		default:
			out = append(out, replacementChar)
			i++
			continue
		}
		j := i + 1
		valid := true
		for k := 0; k < need; k++ {
			if j >= len(b) || b[j] < lower || b[j] > upper {
				valid = false
				break
			}
			lower, upper = 0x80, 0xBF
			cp = cp<<6 | rune(b[j]&0x3F)
			j++
		}
		i = j
		if !valid {
			out = append(out, replacementChar)
			continue
		}
		out = appendRune(out, cp)
	}
	return out
}

// appendRune appends the UTF-16 encoding of r.
func appendRune(out []uint16, r rune) []uint16 {
	if r >= 0x10000 {
		hi, lo := utf16.EncodeRune(r)
		return append(out, uint16(hi), uint16(lo))
	}
	return append(out, uint16(r))
}

// encodeUTF8 is Buffer.from(str, 'utf8'): lone surrogates become U+FFFD.
func encodeUTF8(units []uint16) []byte {
	out := make([]byte, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := rune(units[i])
		switch {
		case u >= 0xD800 && u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000:
			u = utf16.DecodeRune(u, rune(units[i+1]))
			i++
		case u >= 0xD800 && u < 0xE000:
			u = replacementChar
		}
		out = appendUTF8(out, u)
	}
	return out
}

// appendUTF8 appends the UTF-8 encoding of a valid code point.
func appendUTF8(out []byte, r rune) []byte {
	switch {
	case r < 0x80:
		return append(out, byte(r))
	case r < 0x800:
		return append(out, 0xC0|byte(r>>6), 0x80|byte(r&0x3F))
	case r < 0x10000:
		return append(out, 0xE0|byte(r>>12), 0x80|byte(r>>6&0x3F), 0x80|byte(r&0x3F))
	default:
		return append(out, 0xF0|byte(r>>18), 0x80|byte(r>>12&0x3F), 0x80|byte(r>>6&0x3F), 0x80|byte(r&0x3F))
	}
}

// bufferToString is Buffer.prototype.toString() (utf8 with replacement) as a Go string.
func bufferToString(b []byte) string {
	return string(encodeUTF8(decodeUTF8(b)))
}

// isJSWhitespace reports whether r is matched by \s in a JavaScript regular expression.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// isLineTerminator reports whether r ends a line for the ^, $ and . of a JavaScript regular expression.
func isLineTerminator(r rune) bool {
	return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029
}

// jsJoin follows Array.prototype.join(",") over strings.
func jsJoin(parts []string) string {
	return strings.Join(parts, ",")
}
