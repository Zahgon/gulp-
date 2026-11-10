package vfs

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// This file implements vinyl-fs's text codecs (lib/codecs.js) with the iconv-lite 0.6.3 codecs it reaches into: the
// Node.js internal codecs (utf8, cesu8, ucs2/utf16le, binary/latin1, base64, hex), utf16be, utf16 with BOM detection,
// utf32le, utf32be, utf32 with BOM detection, and the single-byte family generated into codec_tables.go.

// defaultEncoding follows vinyl-fs's DEFAULT_ENCODING.
const defaultEncoding = "utf8"

// textDecoder is an iconv-lite decoder: write converts bytes to UTF-16 code units and end flushes what is left.
type textDecoder interface {
	write(b []byte) []uint16
	end() []uint16
}

// codec follows vinyl-fs's Codec wrapper around an iconv-lite codec.
type codec struct {
	enc        string // codec.enc || encoding: compared with "utf8" to decide whether to convert
	bomAware   bool
	newDecoder func() textDecoder
	encode     func(units []uint16) (body, tail []byte) // encoder.write(str) and encoder.end()
}

// errConcatList is the TypeError Codec.prototype.encode throws: it calls Buffer.concat(buf, end) whenever the
// encoder leaves a tail (base64 with an incomplete quad, utf32 with a trailing high surrogate).
var errConcatList = errors.New(`The "list" argument must be an instance of Array. Received an instance of Buffer`)

// encodeBuffer is Codec.prototype.encode as write-buffer uses it.
func (c *codec) encodeBuffer(units []uint16) ([]byte, error) {
	body, tail := c.encode(units)
	if len(tail) > 0 {
		return nil, errConcatList
	}
	return body, nil
}

// encodeStream is Codec.prototype.encodeStream applied to the whole text: every chunk is pushed, and an empty
// text never reaches the encoder's write (so no BOM is written for it).
func (c *codec) encodeStream(units []uint16) []byte {
	if len(units) == 0 {
		return nil
	}
	body, tail := c.encode(units)
	return append(body, tail...)
}

// whole adapts an encoder without state to the body/tail form.
func whole(fn func([]uint16) []byte) func([]uint16) ([]byte, []byte) {
	return func(units []uint16) ([]byte, []byte) { return fn(units), nil }
}

// canonicalEncodingRe is the expression iconv-lite strips from an encoding name.
var canonicalEncodingRe = regexp.MustCompile(`:[0-9]{4}$|[^0-9a-z]`)

// canonicalEncoding is iconv._canonicalizeEncoding.
func canonicalEncoding(name string) string {
	return canonicalEncodingRe.ReplaceAllString(strings.ToLower(name), "")
}

// getCodec follows vinyl-fs's getCodec: nil when iconv-lite does not know the encoding (or it belongs to a family
// this package leaves out).
func getCodec(encoding string) *codec {
	if encoding == "" {
		return nil
	}
	kind, ok := codecNames[canonicalEncoding(encoding)]
	if !ok {
		return nil
	}
	return codecFor(kind, encoding)
}

// codecFor builds the codec of the given kind; encoding is the caller's spelling, used as enc for codecs that
// have none of their own.
func codecFor(kind, encoding string) *codec {
	switch kind {
	case "utf8":
		return &codec{enc: "utf8", bomAware: true, newDecoder: newUTF8Decoder, encode: whole(encodeUTF8)}
	case "cesu8":
		return &codec{enc: "utf8", bomAware: true, newDecoder: func() textDecoder { return &cesu8Decoder{} },
			encode: whole(encodeCESU8)}
	case "ucs2":
		return &codec{enc: "ucs2", bomAware: true, newDecoder: newUCS2Decoder, encode: whole(encodeUCS2)}
	case "binary":
		return &codec{enc: "binary", newDecoder: func() textDecoder { return &latin1Decoder{} },
			encode: whole(encodeLatin1)}
	case "base64":
		return &codec{enc: "base64", newDecoder: func() textDecoder { return &base64Decoder{} },
			encode: encodeBase64Chunked}
	case "hex":
		return &codec{enc: "hex", newDecoder: func() textDecoder { return &hexDecoder{} }, encode: whole(nodeHexDecode)}
	case "utf16be":
		return &codec{enc: encoding, bomAware: true, newDecoder: newUTF16BEDecoder, encode: whole(encodeUTF16BE)}
	case "utf16":
		return &codec{enc: encoding, newDecoder: func() textDecoder { return &utf16AutoDecoder{} },
			encode: whole(func(units []uint16) []byte { return encodeUCS2(append([]uint16{0xFEFF}, units...)) })}
	case "utf32le", "utf32be":
		le := kind == "utf32le"
		return &codec{enc: encoding, bomAware: true,
			newDecoder: func() textDecoder { return &utf32Decoder{le: le} },
			encode:     func(units []uint16) ([]byte, []byte) { return encodeUTF32(units, le) }}
	case "utf32":
		return &codec{enc: encoding, newDecoder: func() textDecoder { return &utf32AutoDecoder{} },
			encode: func(units []uint16) ([]byte, []byte) {
				return encodeUTF32(append([]uint16{0xFEFF}, units...), true)
			}}
	}
	if name, ok := strings.CutPrefix(kind, "sbcs:"); ok {
		if table := sbcsTables[name]; table != nil {
			return newSBCSCodec(encoding, table)
		}
	}
	return nil
}

// decode is Codec.prototype.decode: the decoder, wrapped in iconv-lite's StripBOM when removeBOM is set.
func (c *codec) decode(b []byte, removeBOM bool) []uint16 {
	d := c.newDecoder()
	if removeBOM {
		d = &stripBOM{inner: d}
	}
	return append(d.write(b), d.end()...)
}

// stripBOM follows iconv-lite's StripBOMWrapper: the first non-empty chunk loses a leading U+FEFF.
type stripBOM struct {
	inner textDecoder
	pass  bool
}

func (s *stripBOM) write(b []byte) []uint16 {
	res := s.inner.write(b)
	if s.pass || len(res) == 0 {
		return res
	}
	if res[0] == 0xFEFF {
		res = res[1:]
	}
	s.pass = true
	return res
}

func (s *stripBOM) end() []uint16 { return s.inner.end() }

// wholeDecoder adapts a stateless whole-buffer conversion to textDecoder for decoders that only see one buffer.
type wholeDecoder struct {
	convert func(b []byte) []uint16
}

func (w *wholeDecoder) write(b []byte) []uint16 {
	return w.convert(b)
}

func (w *wholeDecoder) end() []uint16 { return nil }

// newUTF8Decoder follows Node's StringDecoder('utf8') applied to one buffer.
func newUTF8Decoder() textDecoder { return &wholeDecoder{convert: decodeUTF8} }

// newUCS2Decoder is StringDecoder('ucs2'): a trailing odd byte is dropped.
func newUCS2Decoder() textDecoder {
	return &wholeDecoder{convert: func(b []byte) []uint16 {
		out := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			out = append(out, binary.LittleEndian.Uint16(b[i:]))
		}
		return out
	}}
}

// newUTF16BEDecoder follows iconv-lite's Utf16BEDecoder applied to one buffer: a trailing odd byte is dropped.
func newUTF16BEDecoder() textDecoder {
	return &wholeDecoder{convert: func(b []byte) []uint16 {
		out := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			out = append(out, binary.BigEndian.Uint16(b[i:]))
		}
		return out
	}}
}

// latin1Decoder is StringDecoder('latin1').
type latin1Decoder struct{}

func (latin1Decoder) write(b []byte) []uint16 {
	out := make([]uint16, len(b))
	for i, c := range b {
		out[i] = uint16(c)
	}
	return out
}

func (latin1Decoder) end() []uint16 { return nil }

// base64Decoder is StringDecoder('base64') applied to one buffer: padded standard base64.
type base64Decoder struct{}

func (base64Decoder) write(b []byte) []uint16 {
	return asciiUnits(base64.StdEncoding.EncodeToString(b))
}

func (base64Decoder) end() []uint16 { return nil }

// hexDecoder is StringDecoder('hex').
type hexDecoder struct{}

func (hexDecoder) write(b []byte) []uint16 { return asciiUnits(hex.EncodeToString(b)) }

func (hexDecoder) end() []uint16 { return nil }

// asciiUnits converts an ASCII string to code units.
func asciiUnits(s string) []uint16 {
	out := make([]uint16, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = uint16(s[i])
	}
	return out
}

// cesu8Decoder follows iconv-lite's InternalDecoderCesu8, used because Node.js has no CESU-8 decoder.
type cesu8Decoder struct {
	acc, contBytes, accBytes int
}

func (d *cesu8Decoder) write(b []byte) []uint16 {
	var res []uint16
	for _, cur := range b {
		if cur&0xC0 != 0x80 {
			if d.contBytes > 0 {
				res = append(res, replacementChar)
				d.contBytes = 0
			}
			switch {
			case cur < 0x80:
				res = append(res, uint16(cur))
			case cur < 0xE0:
				d.acc, d.contBytes, d.accBytes = int(cur&0x1F), 1, 1
			case cur < 0xF0:
				d.acc, d.contBytes, d.accBytes = int(cur&0x0F), 2, 1
			default:
				res = append(res, replacementChar)
			}
			continue
		}
		if d.contBytes == 0 {
			res = append(res, replacementChar)
			continue
		}
		d.acc = d.acc<<6 | int(cur&0x3F)
		d.contBytes--
		d.accBytes++
		if d.contBytes == 0 {
			switch {
			case d.accBytes == 2 && d.acc < 0x80 && d.acc > 0, d.accBytes == 3 && d.acc < 0x800:
				res = append(res, replacementChar)
			default:
				res = append(res, uint16(d.acc))
			}
		}
	}
	return res
}

// end reproduces the original's `var res = 0; res += defaultChar`, which yields "0�" for a truncated
// sequence and nothing otherwise.
func (d *cesu8Decoder) end() []uint16 {
	if d.contBytes > 0 {
		return []uint16{'0', replacementChar}
	}
	return nil
}

// utf16AutoDecoder follows iconv-lite's Utf16Decoder for one buffer: the byte order comes from the BOM or the
// ASCII heuristic, and the chosen decoder strips a leading BOM.
type utf16AutoDecoder struct{ buf []byte }

func (d *utf16AutoDecoder) write(b []byte) []uint16 {
	d.buf = append(d.buf, b...)
	return nil
}

func (d *utf16AutoDecoder) end() []uint16 {
	var inner textDecoder
	if detectUTF16(d.buf) == "be" {
		inner = newUTF16BEDecoder()
	} else {
		inner = newUCS2Decoder()
	}
	s := &stripBOM{inner: inner}
	return append(s.write(d.buf), s.end()...)
}

// detectUTF16 is utf16.js detectEncoding with no default encoding.
func detectUTF16(b []byte) string {
	asciiLE, asciiBE := 0, 0
	for i, chars := 0, 0; i+1 < len(b) && chars < 100; i, chars = i+2, chars+1 {
		if chars == 0 {
			if b[0] == 0xFF && b[1] == 0xFE {
				return "le"
			}
			if b[0] == 0xFE && b[1] == 0xFF {
				return "be"
			}
		}
		if b[i] == 0 && b[i+1] != 0 {
			asciiBE++
		}
		if b[i] != 0 && b[i+1] == 0 {
			asciiLE++
		}
	}
	if asciiBE > asciiLE {
		return "be"
	}
	return "le"
}

// utf32Decoder follows iconv-lite's Utf32Decoder for one buffer: trailing bytes that do not form a unit are dropped
// and values outside Unicode become U+FFFD.
type utf32Decoder struct{ le bool }

func (d *utf32Decoder) write(b []byte) []uint16 {
	out := make([]uint16, 0, len(b)/2)
	for i := 0; i+3 < len(b); i += 4 {
		var cp uint32
		if d.le {
			cp = binary.LittleEndian.Uint32(b[i:])
		} else {
			cp = binary.BigEndian.Uint32(b[i:])
		}
		if cp > 0x10FFFF {
			cp = replacementChar
		}
		if cp >= 0x10000 {
			cp -= 0x10000
			out = append(out, uint16(0xD800|cp>>10), uint16(0xDC00|cp&0x3FF))
			continue
		}
		out = append(out, uint16(cp))
	}
	return out
}

func (d *utf32Decoder) end() []uint16 { return nil }

// utf32AutoDecoder follows iconv-lite's Utf32AutoDecoder for one buffer.
type utf32AutoDecoder struct{ buf []byte }

func (d *utf32AutoDecoder) write(b []byte) []uint16 {
	d.buf = append(d.buf, b...)
	return nil
}

func (d *utf32AutoDecoder) end() []uint16 {
	s := &stripBOM{inner: &utf32Decoder{le: detectUTF32(d.buf) == "le"}}
	return append(s.write(d.buf), s.end()...)
}

// detectUTF32 is utf32.js detectEncoding with no default encoding.
func detectUTF32(b []byte) string {
	invalidLE, invalidBE, bmpLE, bmpBE := 0, 0, 0, 0
	for i, chars := 0, 0; i+3 < len(b) && chars < 100; i, chars = i+4, chars+1 {
		c := b[i : i+4]
		if chars == 0 {
			if c[0] == 0xFF && c[1] == 0xFE && c[2] == 0 && c[3] == 0 {
				return "le"
			}
			if c[0] == 0 && c[1] == 0 && c[2] == 0xFE && c[3] == 0xFF {
				return "be"
			}
		}
		if c[0] != 0 || c[1] > 0x10 {
			invalidBE++
		}
		if c[3] != 0 || c[2] > 0x10 {
			invalidLE++
		}
		if c[0] == 0 && c[1] == 0 && (c[2] != 0 || c[3] != 0) {
			bmpBE++
		}
		if (c[0] != 0 || c[1] != 0) && c[2] == 0 && c[3] == 0 {
			bmpLE++
		}
	}
	if bmpBE-invalidBE > bmpLE-invalidLE {
		return "be"
	}
	return "le"
}

// encodeCESU8 follows iconv-lite's InternalEncoderCesu8.
func encodeCESU8(units []uint16) []byte {
	out := make([]byte, 0, len(units)*3)
	for _, u := range units {
		switch {
		case u < 0x80:
			out = append(out, byte(u))
		case u < 0x800:
			out = append(out, 0xC0|byte(u>>6), 0x80|byte(u&0x3F))
		default:
			out = append(out, 0xE0|byte(u>>12), 0x80|byte(u>>6&0x3F), 0x80|byte(u&0x3F))
		}
	}
	return out
}

// encodeUCS2 is Buffer.from(str, 'ucs2').
func encodeUCS2(units []uint16) []byte {
	out := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2*i:], u)
	}
	return out
}

// encodeUTF16BE follows iconv-lite's Utf16BEEncoder.
func encodeUTF16BE(units []uint16) []byte {
	out := make([]byte, 2*len(units))
	for i, u := range units {
		binary.BigEndian.PutUint16(out[2*i:], u)
	}
	return out
}

// encodeLatin1 is Buffer.from(str, 'latin1'): the low byte of each code unit.
func encodeLatin1(units []uint16) []byte {
	out := make([]byte, len(units))
	for i, u := range units {
		out[i] = byte(u)
	}
	return out
}

// encodeUTF32 follows iconv-lite's Utf32Encoder: write combines surrogate pairs and writes lone surrogates as they
// are, and end writes a trailing high surrogate.
func encodeUTF32(units []uint16, le bool) (body, tail []byte) {
	put := func(out []byte, v uint32) []byte {
		if le {
			return binary.LittleEndian.AppendUint32(out, v)
		}
		return binary.BigEndian.AppendUint32(out, v)
	}
	out := make([]byte, 0, 4*len(units))
	var high uint32
	for _, u := range units {
		code := uint32(u)
		isHigh := code >= 0xD800 && code < 0xDC00
		isLow := code >= 0xDC00 && code < 0xE000
		if high != 0 {
			if isHigh || !isLow {
				out = put(out, high)
			} else {
				out = put(out, (high-0xD800)<<10|(code-0xDC00)+0x10000)
				high = 0
				continue
			}
		}
		if isHigh {
			high = code
		} else {
			out = put(out, code)
			high = 0
		}
	}
	if high != 0 {
		tail = put(nil, high)
	}
	return out, tail
}

// encodeBase64Chunked follows iconv-lite's InternalEncoderBase64: complete quads of characters are decoded by write
// and the remainder by end, each with Buffer.from(str, 'base64').
func encodeBase64Chunked(units []uint16) (body, tail []byte) {
	complete := len(units) - len(units)%4
	return nodeBase64Decode(units[:complete]), nodeBase64Decode(units[complete:])
}

// unbase64 maps a byte to its base64 value (both alphabets), or 0xFF when it is not a base64 character.
func unbase64(c byte) byte {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A'
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26
	case c >= '0' && c <= '9':
		return c - '0' + 52
	case c == '+' || c == '-':
		return 62
	case c == '/' || c == '_':
		return 63
	}
	return 0xFF
}

// nodeBase64Decode is Buffer.from(str, 'base64'): code units are truncated to a byte, characters outside both
// alphabets are skipped, "=" stops decoding, and the result is cut to the length Node estimates up front.
func nodeBase64Decode(units []uint16) []byte {
	n := len(units)
	if n > 0 && units[n-1] == '=' {
		n--
	}
	if n > 1 && units[n-1] == '=' {
		n--
	}
	size := n * 3 >> 2
	var out []byte
	var group [4]byte
	g := 0
	for _, u := range units {
		c := byte(u)
		v := unbase64(c)
		if v == 0xFF {
			if c == '=' {
				break
			}
			continue
		}
		group[g] = v
		g++
		if g == 4 {
			out = append(out, group[0]<<2|group[1]>>4, group[1]<<4|group[2]>>2, group[2]<<6|group[3])
			g = 0
		}
	}
	switch g {
	case 2:
		out = append(out, group[0]<<2|group[1]>>4)
	case 3:
		out = append(out, group[0]<<2|group[1]>>4, group[1]<<4|group[2]>>2)
	}
	if len(out) > size {
		out = out[:size]
	}
	return out
}

// unhex maps a byte to its hex value or 0xFF.
func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0xFF
}

// nodeHexDecode is Buffer.from(str, 'hex'): pairs are decoded until the first invalid one.
func nodeHexDecode(units []uint16) []byte {
	out := make([]byte, 0, len(units)/2)
	for i := 0; i+1 < len(units); i += 2 {
		hi, lo := unhex(byte(units[i])), unhex(byte(units[i+1]))
		if hi == 0xFF || lo == 0xFF {
			break
		}
		out = append(out, hi<<4|lo)
	}
	return out
}

// newSBCSCodec follows iconv-lite's SBCSCodec: decoding maps bytes through the table, encoding maps each code unit
// back to the last byte that decodes to it, or "?".
func newSBCSCodec(encoding string, table *[256]uint16) *codec {
	return &codec{
		enc: encoding,
		newDecoder: func() textDecoder {
			return &wholeDecoder{convert: func(b []byte) []uint16 {
				out := make([]uint16, len(b))
				for i, c := range b {
					out[i] = table[c]
				}
				return out
			}}
		},
		encode: whole(func(units []uint16) []byte {
			reverse := make(map[uint16]byte, 256)
			for i, u := range table {
				reverse[u] = byte(i)
			}
			out := make([]byte, len(units))
			for i, u := range units {
				c, ok := reverse[u]
				if !ok {
					c = '?'
				}
				out[i] = c
			}
			return out
		}),
	}
}
