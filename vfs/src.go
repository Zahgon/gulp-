package vfs

import (
	"errors"
	"io"
	"math"
	"syscall"

	"github.com/gulpjs/gulp/internal/glob"
	"github.com/gulpjs/gulp/vinyl"
)

// srcConfig follows vinyl-fs's lib/src/options.js.
var srcConfig = map[string]optionDef{
	"buffer":          {types: []string{typeBoolean}, def: true},
	"read":            {types: []string{typeBoolean}, def: true},
	"since":           {types: []string{typeDate}},
	"removeBOM":       {types: []string{typeBoolean}, def: true},
	"encoding":        {types: []string{typeString, typeBoolean}, def: defaultEncoding},
	"sourcemaps":      {types: []string{typeBoolean}, def: false},
	"resolveSymlinks": {types: []string{typeBoolean}, def: true},
}

// Src returns a stream of the files matching globs: globs is a non-empty string or a non-empty []string of non-empty strings
// (a []any of strings is accepted too). The returned error is what src() throws synchronously; errors met while
// walking or reading come from Wait. Files piped into the stream pass through before the matched files.
func Src(globs any, opts Options) (*Stream, error) {
	r := newResolver(srcConfig, opts)
	list, ok := validGlobs(globs)
	if !ok {
		return nil, errors.New("Invalid glob argument: " + jsString(globs))
	}
	for i, g := range list {
		list[i] = glob.NormalizePath(g, false)
	}
	gs, err := newGlobStream(list, opts)
	if err != nil {
		return nil, err
	}
	return &Stream{source: func(emit emitFunc) error {
		entries, walkErr := gs.run()
		var times stageTimes
		for _, e := range entries {
			f, err := r.srcFile(e, &times)
			if errors.Is(err, errSymlinkCycle) {
				// The original never finishes resolving the cycle, so no later file leaves the pipeline; its
				// walker reports the loop as ELOOP, and that error is returned here when the walker did not.
				if walkErr == nil {
					return nodeErr("stat", e.path, syscall.ELOOP)
				}
				break
			}
			if err != nil {
				if walkErr != nil && walkErr.at <= times.failedAt {
					return walkErr.err
				}
				return err
			}
			if walkErr != nil && times.stage2 >= walkErr.at {
				break
			}
			if f == nil {
				continue
			}
			if err := emit(f); err != nil {
				return err
			}
		}
		if walkErr != nil {
			return walkErr.err
		}
		return nil
	}}, nil
}

// stageTimes tracks the step clocks of the two asynchronous src stages (resolve-symlinks, then read-contents),
// which process files in order and overlap like the original's streamx pipeline.
type stageTimes struct {
	stage1, stage2 int
	failedAt       int // the step at which the last srcFile call failed
}

// validGlobs follows is-valid-glob 1.0.0 returning the globs as a slice.
func validGlobs(globs any) ([]string, bool) {
	switch g := globs.(type) {
	case string:
		return []string{g}, g != ""
	case []string:
		if len(g) == 0 {
			return nil, false
		}
		for _, s := range g {
			if s == "" {
				return nil, false
			}
		}
		return append([]string(nil), g...), true
	case []any:
		if len(g) == 0 {
			return nil, false
		}
		out := make([]string, len(g))
		for i, e := range g {
			s, ok := e.(string)
			if !ok || s == "" {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

// srcFile runs one glob entry through the src pipeline: wrap-vinyl, resolve-symlinks, prepare (since),
// read-contents and sourcemap, advancing the stage clocks by one step per file system call. A nil file without
// error means the since option skipped it.
func (r *resolver) srcFile(e globEntry, times *stageTimes) (*vinyl.File, error) {
	f := vinyl.New(vinyl.Options{Cwd: e.cwd, Base: e.base, Path: e.path})
	clock := &stepClock{now: max(e.at, times.stage1)}
	err := r.resolveSymlinks(f, clock)
	times.stage1 = clock.now
	if err != nil {
		times.failedAt = clock.now
		return nil, err
	}
	if since, ok := r.resolve("since", f).(float64); ok && f.Stat != nil {
		newest := math.Max(float64(f.Stat.Mtime.UnixMilli()), float64(f.Stat.Ctime.UnixMilli()))
		if newest <= since {
			return nil, nil
		}
	}
	clock.now = max(clock.now, times.stage2)
	err = r.readContents(f, clock)
	times.stage2 = clock.now
	if err != nil {
		times.failedAt = clock.now
		return nil, err
	}
	if r.boolean("sourcemaps", f) {
		if err := addSourceMap(f); err != nil {
			times.failedAt = clock.now
			return nil, err
		}
	}
	return f, nil
}

// resolveSymlinks follows lib/src/resolve-symlinks.js: the file gets its lstat, and a link (when resolveSymlinks is
// on) gets the stat of its final target. Link targets are resolved against the process working directory, as
// the original's fs.lstat(readlink(...)) does.
func (r *resolver) resolveSymlinks(f *vinyl.File, clock *stepClock) error {
	clock.step()
	st, err := lstatFile(f.Path())
	if err != nil {
		return err
	}
	f.Stat = st
	if !st.IsSymbolicLink() || !r.boolean("resolveSymlinks", f) {
		return nil
	}
	hard, err := findSymlinkHardpath(f.Path(), clock)
	if err != nil {
		return err
	}
	clock.step()
	st, err = statFile(hard)
	if err != nil {
		return err
	}
	f.Stat = st
	return nil
}

// errSymlinkCycle marks a link chain that loops. The original's findSymlinkHardpath follows such a chain
// forever, so the file never leaves the pipeline; the walker's realpath of the same link then fails the stream
// with ELOOP.
var errSymlinkCycle = errors.New("symbolic link cycle")

// findSymlinkHardpath is file-operations findSymlinkHardpath.
func findSymlinkHardpath(p string, clock *stepClock) (string, error) {
	seen := map[string]bool{}
	for {
		clock.step()
		target, err := readlink(p)
		if err != nil {
			return "", err
		}
		clock.step()
		st, err := lstatFile(target)
		if err != nil {
			return "", err
		}
		if !st.IsSymbolicLink() {
			return target, nil
		}
		if seen[target] {
			return "", errSymlinkCycle
		}
		seen[target] = true
		p = target
	}
}

// readContents follows lib/src/read-contents.
func (r *resolver) readContents(f *vinyl.File, clock *stepClock) error {
	if !r.boolean("read", f) {
		return nil
	}
	if f.IsDirectory() {
		return nil
	}
	if f.Stat != nil && f.Stat.IsSymbolicLink() {
		clock.step()
		target, err := readlink(f.Path())
		if err != nil {
			return err
		}
		f.SetSymlink(target)
		return nil
	}
	enc, c, err := r.codecFor(f)
	if err != nil {
		return err
	}
	removeBOM := c != nil && c.bomAware && r.boolean("removeBOM", f)
	if r.boolean("buffer", f) {
		// fs.readFile is open, fstat, read and close; a failure is reported by open.
		clock.step()
		data, err := readFile(f.Path())
		if err != nil {
			return err
		}
		clock.now += 3
		f.SetContents(decodeContents(data, enc, c, removeBOM))
		return nil
	}
	f.SetContentsStream(newFileStream(f.Path(), enc, c, removeBOM))
	return nil
}

// codecFor resolves the encoding option of f: enc is "" when contents are binary, and an unknown encoding is
// vinyl-fs's "Unsupported encoding" error.
func (r *resolver) codecFor(f *vinyl.File) (string, *codec, error) {
	v := r.resolve("encoding", f)
	if !truthy(v) {
		return "", nil, nil
	}
	enc := jsString(v)
	c := getCodec(enc)
	if c == nil {
		return "", nil, errors.New("Unsupported encoding: " + enc)
	}
	return enc, c, nil
}

// decodeContents converts file bytes to UTF-8 the way read-buffer does: only when a BOM is to be removed or the
// encoding is not utf8.
func decodeContents(data []byte, enc string, c *codec, removeBOM bool) []byte {
	if enc == "" || (!removeBOM && c.enc == defaultEncoding) {
		return data
	}
	return encodeUTF8(c.decode(data, removeBOM))
}

// fileStream is the Composer stream read-stream builds: the file is read and decoded when first read.
type fileStream struct {
	path      string
	enc       string
	codec     *codec
	removeBOM bool
	rest      []byte
	err       error
	opened    bool
}

func newFileStream(path, enc string, c *codec, removeBOM bool) *fileStream {
	return &fileStream{path: path, enc: enc, codec: c, removeBOM: removeBOM}
}

// Read implements io.Reader.
func (s *fileStream) Read(p []byte) (int, error) {
	if !s.opened {
		s.opened = true
		data, err := readFile(s.path)
		if err != nil {
			s.err = err
		} else {
			s.rest = decodeContents(data, s.enc, s.codec, s.removeBOM)
		}
	}
	if len(s.rest) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		return 0, io.EOF
	}
	n := copy(p, s.rest)
	s.rest = s.rest[n:]
	return n, nil
}

// StreamName reports the constructor name of the original stream (stream-composer's Composer).
func (s *fileStream) StreamName() string { return "Composer" }
