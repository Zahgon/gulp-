package vfs

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vinyl"
)

// This file loads and writes source maps as vinyl-sourcemap 2.0.0 does (add and write), with the parts of convert-
// source-map 2.0.0 it uses.

// errNoSources is the TypeError now-and-later throws when a loaded map has no sources array.
var errNoSources = errors.New("Cannot convert undefined or null to object")

// sourceMapState is the state object of vinyl-sourcemap's addSourceMaps.
type sourceMapState struct {
	path    string
	m       map[string]any
	keys    []string
	content string
}

// addSourceMap is sourcemap.add(file): it loads an inline or external source map, or builds an identity map.
func addSourceMap(f *vinyl.File) error {
	if f.IsNull() || f.SourceMap() != nil {
		return nil
	}
	var raw []byte
	if f.IsStream() {
		data, err := io.ReadAll(f.ContentsStream())
		if err != nil {
			return err
		}
		raw = data
	} else {
		raw = f.Contents()
	}
	st := &sourceMapState{content: bufferToString(raw)}
	if err := loadSourceMap(f, st); err != nil {
		return err
	}
	if err := fixImportedSourceMap(f, st); err != nil {
		return err
	}
	rel, err := f.Relative()
	if err != nil {
		return err
	}
	if st.m == nil {
		st.m = map[string]any{
			"version":        float64(3),
			"names":          []any{},
			"mappings":       "",
			"sources":        []any{normalizeRelpath(rel)},
			"sourcesContent": []any{st.content},
		}
		st.keys = []string{"version", "names", "mappings", "sources", "sourcesContent"}
	}
	setKey(st, "file", normalizeRelpath(rel))
	f.SetSourceMapKeys(st.m, st.keys)
	return nil
}

// setKey assigns a property, appending the key to the order when it is new.
func setKey(st *sourceMapState, key string, v any) {
	if _, ok := st.m[key]; !ok {
		st.keys = append(st.keys, key)
	}
	st.m[key] = v
}

// replaceContents stores the source without its map comment, as a buffer or a new readable stream.
func replaceContents(f *vinyl.File, content string) {
	if f.IsStream() {
		f.SetContentsStream(&namedReader{Reader: strings.NewReader(content), name: "Readable"})
		return
	}
	f.SetContents([]byte(content))
}

// namedReader is a reader that prints as the given JavaScript stream type.
type namedReader struct {
	io.Reader
	name string
}

// StreamName reports the constructor name of the original stream.
func (r *namedReader) StreamName() string { return r.name }

// loadSourceMap follows vinyl-sourcemap's loadSourceMap.
func loadSourceMap(f *vinyl.File, st *sourceMapState) error {
	dirname, err := f.Dirname()
	if err != nil {
		return err
	}
	if comment, ok := lastInlineComment(st.content); ok {
		m, keys, err := mapFromComment(comment)
		if err != nil {
			return err
		}
		if m != nil {
			st.m, st.keys = m, keys
			st.path = dirname
			st.content = removeInlineComments(st.content)
			replaceContents(f, st.content)
			return nil
		}
	}

	var mapFile string
	if ref, ok := firstMapFileComment(st.content); ok {
		resolved, err := nodepath.Resolve(dirname, ref)
		if err != nil {
			return err
		}
		mapFile = resolved
		st.content = removeMapFileComments(st.content)
		replaceContents(f, st.content)
	} else {
		mapFile = f.Path() + ".map"
	}
	st.path = nodepath.Dirname(mapFile)
	// A missing, unreadable or unparsable map file is not an error: the identity map is built instead.
	data, err := readFile(mapFile)
	if err != nil {
		return nil
	}
	m, keys, err := parseJSONObject(textDecode(data))
	if err != nil {
		return nil
	}
	st.m, st.keys = m, keys
	return nil
}

// textDecode is TextDecoder('utf-8').decode: replacement characters for invalid bytes and the BOM removed.
func textDecode(b []byte) string {
	s := bufferToString(b)
	return strings.TrimPrefix(s, "\uFEFF")
}

// mapFromComment is convert.fromComment(comment).toObject(). A comment whose JSON is null yields a nil map.
func mapFromComment(comment string) (map[string]any, []string, error) {
	if strings.HasPrefix(comment, "/*") {
		comment = "//" + comment[2:]
	}
	comment = strings.TrimSuffix(comment, "*/")
	encoding := "uri"
	if ms := inlineMatches(comment); len(ms) > 0 && ms[0].base64 {
		encoding = "base64"
	}
	data := comment[strings.LastIndexByte(comment, ',')+1:]
	var text string
	if encoding == "base64" {
		text = bufferToString(nodeBase64Decode(utf8ToUnits(data)))
	} else {
		decoded, err := url.PathUnescape(data)
		if err != nil || !utf8.ValidString(decoded) {
			return nil, nil, errURIMalformed
		}
		text = decoded
	}
	return parseJSONObject(text)
}

// errURIMalformed is the URIError decodeURIComponent throws.
var errURIMalformed = errors.New("URI malformed")

// utf8ToUnits converts a Go string to UTF-16 code units.
func utf8ToUnits(s string) []uint16 {
	return decodeUTF8([]byte(s))
}

// fixImportedSourceMap follows vinyl-sourcemap's fixImportedSourceMap.
func fixImportedSourceMap(f *vinyl.File, st *sourceMapState) error {
	if st.m == nil {
		return nil
	}
	contents, _ := st.m["sourcesContent"].([]any)
	if !truthy(st.m["sourcesContent"]) {
		contents = []any{}
	}
	setKey(st, "sourcesContent", contents)
	sources, ok := st.m["sources"].([]any)
	if !ok {
		if st.m["sources"] == nil {
			return errNoSources
		}
		return nil
	}
	sourceRoot, _ := st.m["sourceRoot"].(string)
	base := f.Base()
	assign := func(idx int, v any) {
		for len(contents) <= idx {
			contents = append(contents, nil)
		}
		contents[idx] = v
		st.m["sourcesContent"] = contents
	}
	for idx, s := range sources {
		sourcePath, _ := s.(string)
		var sourceContent any
		if idx < len(contents) && truthy(contents[idx]) {
			sourceContent = contents[idx]
		}
		if isRemoteSource(sourcePath) {
			assign(idx, sourceContent)
			continue
		}
		if sourceContent != nil {
			continue
		}
		if sourceRoot != "" && isRemoteSource(sourceRoot) {
			assign(idx, sourceContent)
			continue
		}
		basePath, err := nodepath.Resolve(base, sourceRoot)
		if err != nil {
			return err
		}
		absPath, err := nodepath.Resolve(st.path, sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		relPath, err := nodepath.Relative(basePath, absPath)
		if err != nil {
			return err
		}
		sources[idx] = normalizeRelpath(relPath)
		if absPath != f.Path() {
			// A source that cannot be read gets null content, as in the original.
			data, err := readFile(absPath)
			if err != nil {
				assign(idx, nil)
				continue
			}
			assign(idx, textDecode(data))
			continue
		}
		assign(idx, st.content)
	}
	return nil
}

// remoteSourceRe follows vinyl-sourcemap's urlRegex.
var remoteSourceRe = regexp.MustCompile(`^(https?|webpack(-[^:]+)?)://`)

func isRemoteSource(s string) bool { return remoteSourceRe.MatchString(s) }

// normalizeRelpath follows vinyl-sourcemap's normalizeRelpath: posix separators, normalised.
func normalizeRelpath(p string) string {
	return nodepath.Normalize(strings.ReplaceAll(p, "\\", "/"))
}

// inlineParts describes one match of convert-source-map's commentRegex.
type inlineParts struct {
	start, end int // byte offsets of the whole match
	base64     bool
}

// matchInlineComment tries commentRegex (/^\s*?\/[\/\*][@#]\s+?sourceMappingURL=data:(...)?(?:;(base64))?,(.*?)$/m)
// at byte offset start, which must be a line start.
func matchInlineComment(s string, start int) (inlineParts, bool) {
	i := start
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isJSWhitespace(r) {
			break
		}
		i += size
	}
	if !strings.HasPrefix(s[i:], "/") || len(s) < i+3 {
		return inlineParts{}, false
	}
	if s[i+1] != '/' && s[i+1] != '*' {
		return inlineParts{}, false
	}
	if s[i+2] != '@' && s[i+2] != '#' {
		return inlineParts{}, false
	}
	i += 3
	ws := i
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isJSWhitespace(r) {
			break
		}
		i += size
	}
	const marker = "sourceMappingURL=data:"
	if i == ws || !strings.HasPrefix(s[i:], marker) {
		return inlineParts{}, false
	}
	i += len(marker)
	after, isBase64, ok := matchDataPrefix(s, i)
	if !ok {
		return inlineParts{}, false
	}
	end := after
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		if isLineTerminator(r) {
			break
		}
		end += size
	}
	return inlineParts{start: start, end: end, base64: isBase64}, true
}

// matchDataPrefix matches (((?:application|text)\/json)(?:;charset=([^;,]+?)?)?)?(?:;(base64))?, at i with the
// regular expression's backtracking order and returns the offset after the comma.
func matchDataPrefix(s string, i int) (after int, isBase64 bool, ok bool) {
	tail := func(j int) (int, bool, bool) {
		if strings.HasPrefix(s[j:], ";base64,") {
			return j + len(";base64,"), true, true
		}
		if strings.HasPrefix(s[j:], ",") {
			return j + 1, false, true
		}
		return 0, false, false
	}
	for _, mt := range []string{"application/json", "text/json"} {
		if !strings.HasPrefix(s[i:], mt) {
			continue
		}
		j := i + len(mt)
		if strings.HasPrefix(s[j:], ";charset=") {
			k := j + len(";charset=")
			// ([^;,]+?) tried with one character, then more, before the empty alternative.
			for n := k; n < len(s) && s[n] != ';' && s[n] != ','; {
				_, size := utf8.DecodeRuneInString(s[n:])
				n += size
				if a, b, ok := tail(n); ok {
					return a, b, true
				}
			}
			if a, b, ok := tail(k); ok {
				return a, b, true
			}
		}
		if a, b, ok := tail(j); ok {
			return a, b, true
		}
	}
	return tail(i)
}

// isLineStart reports whether ^ matches at byte offset i in multiline mode.
func isLineStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return isLineTerminator(r)
}

// inlineMatches returns every non-overlapping commentRegex match, left to right.
func inlineMatches(s string) []inlineParts {
	var out []inlineParts
	for i := 0; i <= len(s); {
		if isLineStart(s, i) {
			if m, ok := matchInlineComment(s, i); ok {
				out = append(out, m)
				if m.end > i {
					i = m.end
					continue
				}
			}
		}
		if i == len(s) {
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return out
}

// lastInlineComment is content.match(commentRegex).pop().
func lastInlineComment(s string) (string, bool) {
	ms := inlineMatches(s)
	if len(ms) == 0 {
		return "", false
	}
	m := ms[len(ms)-1]
	return s[m.start:m.end], true
}

// removeInlineComments is convert.removeComments.
func removeInlineComments(s string) string {
	return removeRanges(s, inlineMatches(s))
}

// removeRanges deletes the matched ranges from s.
func removeRanges(s string, ms []inlineParts) string {
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(s[last:m.start])
		last = m.end
	}
	b.WriteString(s[last:])
	return b.String()
}

// isMapURLChar reports whether r is matched by [^\s'"`].
func isMapURLChar(r rune) bool {
	return !isJSWhitespace(r) && r != '\'' && r != '"' && r != '`'
}

// atLineEnd reports whether $ matches at i in multiline mode.
func atLineEnd(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return isLineTerminator(r)
}

// skipSpaceTab returns the offset after a run of spaces and tabs.
func skipSpaceTab(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// matchMapFileComment tries convert-source-map's mapFileCommentRegex at byte offset i and returns the end of the
// match and the referenced file.
func matchMapFileComment(s string, i int) (end int, ref string, ok bool) {
	const marker = "sourceMappingURL="
	if strings.HasPrefix(s[i:], "//") && len(s) > i+2 && (s[i+2] == '@' || s[i+2] == '#') {
		j := i + 3
		k := skipSpaceTab(s, j)
		if k > j && strings.HasPrefix(s[k:], marker) {
			u := k + len(marker)
			n := u
			for n < len(s) {
				r, size := utf8.DecodeRuneInString(s[n:])
				if !isMapURLChar(r) {
					break
				}
				n += size
			}
			if e := skipSpaceTab(s, n); n > u && atLineEnd(s, e) {
				return e, s[u:n], true
			}
		}
	}
	if strings.HasPrefix(s[i:], "/*") && len(s) > i+2 && (s[i+2] == '@' || s[i+2] == '#') {
		j := i + 3
		k := skipSpaceTab(s, j)
		if k > j && strings.HasPrefix(s[k:], marker) {
			u := k + len(marker)
			star := strings.IndexByte(s[u:], '*')
			if star > 0 {
				urlEnd := u + star
				for urlEnd > u+1 && (s[urlEnd-1] == ' ' || s[urlEnd-1] == '\t') {
					urlEnd--
				}
				if strings.HasPrefix(s[u+star:], "*/") {
					if e := skipSpaceTab(s, u+star+2); atLineEnd(s, e) {
						return e, s[u:urlEnd], true
					}
				}
			}
		}
	}
	return 0, "", false
}

// mapFileMatches returns every non-overlapping mapFileCommentRegex match.
func mapFileMatches(s string) []inlineParts {
	var out []inlineParts
	for i := 0; i < len(s); i++ {
		if end, _, ok := matchMapFileComment(s, i); ok {
			out = append(out, inlineParts{start: i, end: end})
			i = end - 1
		}
	}
	return out
}

// firstMapFileComment is mapFileCommentRegex.exec(content): the file the first map comment names.
func firstMapFileComment(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if _, ref, ok := matchMapFileComment(s, i); ok {
			return ref, true
		}
	}
	return "", false
}

// removeMapFileComments is convert.removeMapFileComments.
func removeMapFileComments(s string) string {
	return removeRanges(s, mapFileMatches(s))
}

// writeSourceMap is sourcemap.write(file, destPath): it appends the map comment and, with a destination path,
// returns the external map file to write as well.
func writeSourceMap(f *vinyl.File, destPath string, external bool) (*vinyl.File, error) {
	if f.IsNull() || f.SourceMap() == nil {
		return nil, nil
	}
	ext, err := f.Extname()
	if err != nil {
		return nil, err
	}
	multiline := ext == ".css"
	mapJSON := stringifyObject(f.SourceMap(), f.SourceMapKeys())
	var comment string
	var mapFile *vinyl.File
	if !external {
		data := "sourceMappingURL=data:application/json;charset=utf-8;base64," +
			base64.StdEncoding.EncodeToString([]byte(mapJSON))
		comment = commentText(data, multiline)
	} else {
		rel, err := f.Relative()
		if err != nil {
			return nil, err
		}
		dirname, err := f.Dirname()
		if err != nil {
			return nil, err
		}
		sourceMapPath := nodepath.Join(f.Base(), nodepath.Join(destPath, rel)+".map")
		mapFile = vinyl.New(vinyl.Options{
			Cwd:      f.Cwd(),
			Base:     f.Base(),
			Path:     sourceMapPath,
			Contents: []byte(mapJSON),
			Stat:     &vinyl.Stat{Mode: 0},
		})
		mapRel, err := nodepath.Relative(dirname, sourceMapPath)
		if err != nil {
			return nil, err
		}
		comment = commentText("sourceMappingURL="+normalizeRelpath(mapRel), multiline)
	}
	appendComment(f, []byte(comment))
	return mapFile, nil
}

// commentText wraps source map data in a // or /* */ comment.
func commentText(data string, multiline bool) string {
	if multiline {
		return "/*# " + data + " */"
	}
	return "//# " + data
}

// appendComment follows vinyl-sourcemap's append: the comment follows the contents with the contents' last line
// ending, or "\n".
func appendComment(f *vinyl.File, comment []byte) {
	if f.IsBuffer() {
		buf := f.Contents()
		out := make([]byte, 0, len(buf)+len(comment)+2)
		out = append(append(out, buf...), comment...)
		out = append(out, trailingEOL(buf)...)
		f.SetContents(out)
		return
	}
	if f.IsStream() {
		f.SetContentsStream(&appendStream{src: f.ContentsStream(), comment: comment})
	}
}

// trailingEOL is the line ending appendBuffer detects at the end of buf.
func trailingEOL(buf []byte) []byte {
	switch {
	case bytes.HasSuffix(buf, []byte("\r\n")):
		return []byte("\r\n")
	case bytes.HasSuffix(buf, []byte("\n")):
		return []byte("\n")
	case bytes.HasSuffix(buf, []byte("\r")):
		return []byte("\r")
	}
	return []byte("\n")
}

// appendStream follows vinyl-sourcemap's appendStream transform: the source passes through and, at its end, the
// comment and the last line ending seen in the source (or "\n") follow.
type appendStream struct {
	src     io.Reader
	comment []byte
	eol     []byte
	crAtEnd bool
	tail    []byte
	done    bool
}

// Read implements io.Reader.
func (a *appendStream) Read(p []byte) (int, error) {
	if len(a.tail) > 0 {
		n := copy(p, a.tail)
		a.tail = a.tail[n:]
		return n, nil
	}
	if a.done {
		return 0, io.EOF
	}
	n, err := a.src.Read(p)
	a.detect(p[:n])
	if err == io.EOF {
		a.done = true
		eol := a.eol
		if eol == nil {
			eol = []byte("\n")
		}
		a.tail = append(append([]byte{}, a.comment...), eol...)
		if n > 0 {
			return n, nil
		}
		return a.Read(p)
	}
	return n, err
}

// detect tracks the last line ending like the original's detect function.
func (a *appendStream) detect(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if a.crAtEnd {
		if chunk[0] == '\n' {
			a.eol = []byte("\r\n")
		} else {
			a.eol = []byte("\r")
		}
		a.crAtEnd = false
		return
	}
	for i := 0; i < len(chunk); {
		switch {
		case chunk[i] == '\r' && i == len(chunk)-1:
			a.crAtEnd = true
			i++
		case chunk[i] == '\r' && chunk[i+1] == '\n':
			a.eol = []byte("\r\n")
			i += 2
		case chunk[i] == '\r':
			a.eol = []byte("\r")
			i++
		case chunk[i] == '\n':
			a.eol = []byte("\n")
			i++
		default:
			i++
		}
	}
}

// StreamName reports the constructor name of the original stream (a streamx Transform).
func (a *appendStream) StreamName() string { return "Transform" }
