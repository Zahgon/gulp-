// Package vinyl is the virtual file format gulp streams between plugins, with the behaviour of vinyl 3.0.1.
//
// A File carries a path history, a cwd and base, a Stat, and contents that are a byte buffer, a stream (an
// io.Reader) or null. Path setters normalise like Node's path.normalize and strip a trailing separator the way
// vinyl does; getters and setters that need a path return the original's error texts.
package vinyl

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/gulpjs/gulp/internal/nodepath"
)

// Stat holds the file metadata vinyl and vfs use, with the fields and mode bits of a Node.js fs.Stats object.
type Stat struct {
	Mode  uint32    // full st_mode bits as Node reports them (type bits | permission bits), e.g. 0o100644
	Size  int64     // size in bytes
	Mtime time.Time // modification time, millisecond precision like a Node.js Date
	Atime time.Time // access time
	Ctime time.Time // status change time
}

// File type bits of st_mode.
const (
	modeTypeMask  = 0o170000
	modeDirectory = 0o040000
	modeSymlink   = 0o120000
	modeRegular   = 0o100000
)

// IsDirectory reports whether the mode describes a directory, as fs.Stats.isDirectory does.
func (s *Stat) IsDirectory() bool { return s.Mode&modeTypeMask == modeDirectory }

// IsSymbolicLink reports whether the mode describes a symbolic link, as fs.Stats.isSymbolicLink does.
func (s *Stat) IsSymbolicLink() bool { return s.Mode&modeTypeMask == modeSymlink }

// IsFile reports whether the mode describes a regular file, as fs.Stats.isFile does.
func (s *Stat) IsFile() bool { return s.Mode&modeTypeMask == modeRegular }

// Options are the constructor fields of new Vinyl(options).
type Options struct {
	Cwd      string    // "" means the process working directory
	Base     string    // "" means unset (base follows cwd)
	Path     string    // "" means no path
	History  []string  // earlier paths; Path, when set, is appended
	Contents []byte    // nil means null contents unless Stream is set
	Stream   io.Reader // streaming contents
	Stat     *Stat     // file stats, nil when unknown
}

// NamedStream is implemented by streaming contents that report a stream type name (for example "Composer" or
// "Transform"); String prints such contents as <ComposerStream> and the like. Other readers print as
// <ReadableStream>.
type NamedStream interface {
	io.Reader
	// StreamName returns the stream type name String prints, for example "Composer" for <ComposerStream>.
	StreamName() string
}

// File is a vinyl file object.
type File struct {
	// Stat is the file's stat object, nil when unknown.
	Stat *Stat

	history   []string
	cwd       string
	base      string // "" when following cwd
	contents  []byte
	stream    io.Reader
	symlink   string
	sourceMap map[string]any
	mapKeys   []string
}

// Error texts of the original vinyl.
var (
	errCwd      = errors.New("cwd must be a non-empty string.")
	errRelative = errors.New("File.relative is generated from the base and path attributes. Do not modify it.")
)

// noPath builds the "No path specified! Can not <action>." error.
func noPath(action string) error {
	return fmt.Errorf("No path specified! Can not %s.", action)
}

// New builds a file as new Vinyl(opts) does: the history is replayed through the path setter so every entry is
// normalised, cwd defaults to the process working directory and base is dropped when it equals cwd. New cannot
// fail: when the working directory itself cannot be read (it was removed), the file gets an empty cwd, and the
// path operations that need it report that later.
func New(opts Options) *File {
	f := &File{Stat: opts.Stat}
	switch {
	case opts.Stream != nil:
		f.stream = opts.Stream
	case opts.Contents != nil:
		f.contents = opts.Contents
	}
	for _, p := range opts.History {
		f.setPath(p)
	}
	if opts.Path != "" {
		f.setPath(opts.Path)
	}
	cwd := opts.Cwd
	if cwd == "" {
		// An unreadable working directory leaves the file without a cwd rather than inventing one.
		if wd, err := nodepath.Cwd(); err == nil {
			cwd = wd
		}
	}
	f.cwd = cleanPath(cwd)
	f.setBase(opts.Base)
	return f
}

// IsVinyl reports whether v is a vinyl file, as File.isVinyl does.
func IsVinyl(v any) bool {
	f, ok := v.(*File)
	return ok && f != nil
}

// normalize follows vinyl's lib/normalize: path.normalize except that "" stays "".
func normalize(p string) string {
	if p == "" {
		return p
	}
	return nodepath.Normalize(p)
}

// removeTrailingSep follows remove-trailing-separator 1.1.0 on posix: strings shorter than three bytes are kept.
func removeTrailingSep(p string) string {
	if utf16Len(p) < 3 {
		return p
	}
	i := len(p) - 1
	for i > 0 && p[i] == '/' {
		i--
	}
	return p[:i+1]
}

// utf16Len is the JavaScript length of s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// cleanPath is removeTrailingSep(normalize(p)), the treatment every vinyl path setter applies.
func cleanPath(p string) string {
	return removeTrailingSep(normalize(p))
}

// Cwd returns the file's working directory.
func (f *File) Cwd() string { return f.cwd }

// SetCwd sets the working directory. It returns the error "cwd must be a non-empty string." for an empty string.
func (f *File) SetCwd(cwd string) error {
	if cwd == "" {
		return errCwd
	}
	f.cwd = cleanPath(cwd)
	return nil
}

// Base returns the file's base directory, which follows cwd unless set explicitly.
func (f *File) Base() string {
	if f.base != "" {
		return f.base
	}
	return f.cwd
}

// SetBase sets the base directory. An empty string resets base to follow cwd (the original's null/undefined), and
// a value equal to cwd is not stored, exactly as vinyl does. It never fails; the error result keeps the setters
// uniform.
func (f *File) SetBase(base string) error {
	f.setBase(base)
	return nil
}

func (f *File) setBase(base string) {
	if base == "" {
		f.base = ""
		return
	}
	base = cleanPath(base)
	if base == f.cwd {
		f.base = ""
		return
	}
	f.base = base
}

// Path returns the current path, the last history entry, or "" when the file has no path.
func (f *File) Path() string {
	if len(f.history) == 0 {
		return ""
	}
	return f.history[len(f.history)-1]
}

// SetPath sets the path; the normalised value is appended to the history when it is non-empty and differs from
// the current path. It never fails (an empty path is ignored, as in vinyl); the error result keeps the setters
// uniform.
func (f *File) SetPath(p string) error {
	f.setPath(p)
	return nil
}

func (f *File) setPath(p string) {
	p = cleanPath(p)
	if p != "" && p != f.Path() {
		f.history = append(f.history, p)
	}
}

// History returns a copy of the path history, oldest first.
func (f *File) History() []string {
	return append([]string{}, f.history...)
}

// Relative returns the path relative to base, or the error "No path specified! Can not get relative." when the
// file has no path (or the working directory's error when a relative base needs it and it cannot be read).
func (f *File) Relative() (string, error) {
	p := f.Path()
	if p == "" {
		return "", noPath("get relative")
	}
	return nodepath.Relative(f.Base(), p)
}

// SetRelative always returns vinyl's error "File.relative is generated from the base and path attributes. Do not
// modify it.": relative is derived from base and path.
func (f *File) SetRelative(string) error {
	return errRelative
}

// Dirname returns the directory part of the path, or the error "No path specified! Can not get dirname.".
func (f *File) Dirname() (string, error) {
	p := f.Path()
	if p == "" {
		return "", noPath("get dirname")
	}
	return nodepath.Dirname(p), nil
}

// SetDirname moves the file into another directory, keeping its basename. Without a path it returns "No path
// specified! Can not set dirname.".
func (f *File) SetDirname(dirname string) error {
	p := f.Path()
	if p == "" {
		return noPath("set dirname")
	}
	f.setPath(nodepath.Join(dirname, nodepath.Basename(p, "")))
	return nil
}

// Basename returns the last path segment, or the error "No path specified! Can not get basename.".
func (f *File) Basename() (string, error) {
	p := f.Path()
	if p == "" {
		return "", noPath("get basename")
	}
	return nodepath.Basename(p, ""), nil
}

// SetBasename renames the file within its directory. Without a path it returns "No path specified! Can not set
// basename.".
func (f *File) SetBasename(basename string) error {
	p := f.Path()
	if p == "" {
		return noPath("set basename")
	}
	f.setPath(nodepath.Join(nodepath.Dirname(p), basename))
	return nil
}

// Stem returns the basename without its extension, or the error "No path specified! Can not get stem.".
func (f *File) Stem() (string, error) {
	p := f.Path()
	if p == "" {
		return "", noPath("get stem")
	}
	return nodepath.Basename(p, nodepath.Extname(p)), nil
}

// SetStem replaces the basename while keeping the extension. Without a path it returns "No path specified! Can
// not set stem.".
func (f *File) SetStem(stem string) error {
	p := f.Path()
	if p == "" {
		return noPath("set stem")
	}
	f.setPath(nodepath.Join(nodepath.Dirname(p), stem+nodepath.Extname(p)))
	return nil
}

// Extname returns the extension of the basename, including the dot, or the error "No path specified! Can not get
// extname.".
func (f *File) Extname() (string, error) {
	p := f.Path()
	if p == "" {
		return "", noPath("get extname")
	}
	return nodepath.Extname(p), nil
}

// SetExtname replaces the extension as replace-ext 2.0.0 does. Without a path it returns "No path specified! Can
// not set extname.".
func (f *File) SetExtname(extname string) error {
	p := f.Path()
	if p == "" {
		return noPath("set extname")
	}
	f.setPath(replaceExt(p, extname))
	return nil
}

// replaceExt follows replace-ext 2.0.0 on posix.
func replaceExt(p, ext string) string {
	if p == "" {
		return p
	}
	name := nodepath.Basename(p, nodepath.Extname(p)) + ext
	joined := nodepath.Join(nodepath.Dirname(p), name)
	if strings.HasPrefix(p, "./") {
		return "./" + joined
	}
	return joined
}

// Contents returns the buffered contents, nil when the contents are null or a stream.
func (f *File) Contents() []byte { return f.contents }

// SetContents sets buffered contents; nil sets null contents.
func (f *File) SetContents(b []byte) {
	f.contents = b
	f.stream = nil
}

// ContentsStream returns the streaming contents, nil unless the file is streaming.
func (f *File) ContentsStream() io.Reader { return f.stream }

// SetContentsStream sets streaming contents; nil sets null contents.
func (f *File) SetContentsStream(r io.Reader) {
	f.stream = r
	f.contents = nil
}

// IsBuffer reports whether the contents are a buffer.
func (f *File) IsBuffer() bool { return f.contents != nil }

// IsStream reports whether the contents are a stream.
func (f *File) IsStream() bool { return f.stream != nil }

// IsNull reports whether the contents are null.
func (f *File) IsNull() bool { return f.contents == nil && f.stream == nil }

// IsDirectory reports whether the file has null contents and a directory stat.
func (f *File) IsDirectory() bool {
	return f.IsNull() && f.Stat != nil && f.Stat.IsDirectory()
}

// IsSymbolic reports whether the file has null contents and a symbolic link stat.
func (f *File) IsSymbolic() bool {
	return f.IsNull() && f.Stat != nil && f.Stat.IsSymbolicLink()
}

// Symlink returns the symbolic link target, "" when unset.
func (f *File) Symlink() string { return f.symlink }

// SetSymlink sets the symbolic link target, normalised like a path.
func (f *File) SetSymlink(target string) {
	f.symlink = cleanPath(target)
}

// SourceMap returns the source map object, nil when the file has none.
func (f *File) SourceMap() map[string]any { return f.sourceMap }

// SetSourceMap sets the source map object; nil removes it. Keys already known keep their serialisation order and
// new keys follow in sorted order.
func (f *File) SetSourceMap(m map[string]any) {
	f.SetSourceMapKeys(m, nil)
}

// SourceMapKeys returns the top-level keys of the source map in the order JSON.stringify writes them in the
// original (the order the map was parsed or built in). Keys present in the map but missing from the recorded
// order follow in sorted order.
func (f *File) SourceMapKeys() []string {
	return orderedKeys(f.sourceMap, f.mapKeys)
}

// SetSourceMapKeys sets the source map together with the insertion order of its top-level keys, which a Go map
// cannot carry; keys missing from order are appended in sorted order.
func (f *File) SetSourceMapKeys(m map[string]any, order []string) {
	if m == nil {
		f.sourceMap = nil
		f.mapKeys = nil
		return
	}
	if order == nil {
		order = f.mapKeys
	}
	f.sourceMap = m
	f.mapKeys = orderedKeys(m, order)
}

// Clone returns a deep copy as file.clone() does: buffer contents are copied, streaming contents are split so
// both files read the same bytes, and the stat, history and source map are copied.
func (f *File) Clone() *File {
	c := &File{
		history: append([]string(nil), f.history...),
		cwd:     f.cwd,
		base:    f.base,
	}
	if f.Stat != nil {
		st := *f.Stat
		c.Stat = &st
	}
	switch {
	case f.stream != nil:
		f.stream, c.stream = tee(f.stream)
	case f.contents != nil:
		c.contents = append([]byte{}, f.contents...)
	}
	if f.IsSymbolic() {
		c.symlink = f.symlink
	}
	if f.sourceMap != nil {
		c.sourceMap = deepCopy(f.sourceMap).(map[string]any)
		c.mapKeys = append([]string(nil), f.mapKeys...)
	}
	return c
}

// String returns the util.inspect(file) text of the original, for example <File "a/b.txt" <Buffer 68 69>>. The
// quoted relative path is left out when the file has none or it cannot be computed (Relative reports why).
func (f *File) String() string {
	var parts []string
	if f.Path() != "" {
		if rel, err := f.Relative(); err == nil && rel != "" {
			parts = append(parts, `"`+rel+`"`)
		}
	}
	if f.IsBuffer() {
		parts = append(parts, inspectBuffer(f.contents))
	}
	if f.IsStream() {
		parts = append(parts, inspectStream(f.stream))
	}
	return "<File " + strings.Join(parts, " ") + ">"
}

// inspectMaxBytes is buffer.INSPECT_MAX_BYTES.
const inspectMaxBytes = 50

// inspectBuffer is Buffer.prototype.inspect.
func inspectBuffer(b []byte) string {
	n := min(len(b), inspectMaxBytes)
	var sb strings.Builder
	sb.WriteString("<Buffer")
	sb.WriteByte(' ')
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02x", b[i])
	}
	if rest := len(b) - n; rest > 0 {
		fmt.Fprintf(&sb, " ... %d more byte", rest)
		if rest > 1 {
			sb.WriteByte('s')
		}
	}
	sb.WriteByte('>')
	return sb.String()
}

// inspectStream follows vinyl's lib/inspect-stream.
func inspectStream(r io.Reader) string {
	name := "Readable"
	if ns, ok := r.(NamedStream); ok {
		name = ns.StreamName()
	}
	if name == "Stream" {
		name = ""
	}
	return "<" + name + "Stream>"
}
