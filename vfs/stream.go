// Package vfs reads files matching globs into vinyl files and writes or links them back (Src, Dest and
// Symlink), with source maps and text encodings. Its behaviour follows vinyl-fs 4.0.2, glob-stream 8.0.4,
// vinyl-sourcemap 2.0.0 and the iconv-lite 0.6.3 codecs behind the encoding option.
//
// Streams are pipeline stages: Src and FromFiles produce files, Dest, Symlink and Through transform
// them, and Wait drains a pipeline, returning the files in emission order and the first error, which stops the
// pipeline the way a streamx pipeline is destroyed by its first error.
package vfs

import (
	"errors"
	"sync"

	"github.com/gulpjs/gulp/vinyl"
)

// Options is an option bag keyed by the original option names. Values have the original's types: bool, string,
// []string, numbers (any Go integer or float type), time.Time for dates, or a func(*vinyl.File) T where the
// original accepts a function. A value of the wrong type behaves as in the original: resolve-options falls back
// to the default and glob-stream's option validation reports it. A key present with a nil value is the
// original's undefined.
type Options map[string]any

// emitFunc passes a file to the next stage.
type emitFunc func(f *vinyl.File) error

// Stream is one stage of a file pipeline. Pipe and Wait may be called from different goroutines; a pipeline is
// drained once, by the first Wait.
type Stream struct {
	mu        sync.Mutex // guards upstream and consumed
	upstream  *Stream
	source    func(emit emitFunc) error                // files this stage produces after the upstream ones
	transform func(f *vinyl.File, emit emitFunc) error // applied to every upstream file
	consumed  bool
}

// errNilStream is returned when Wait is called on a nil stream.
var errNilStream = errors.New("vfs: nil stream")

// Through returns a transform stage that maps every file with fn. Returning a nil file drops it; returning an
// error stops the pipeline.
func Through(fn func(f *vinyl.File) (*vinyl.File, error)) *Stream {
	return &Stream{transform: func(f *vinyl.File, emit emitFunc) error {
		if fn == nil {
			return emit(f)
		}
		out, err := fn(f)
		if err != nil {
			return err
		}
		if out == nil {
			return nil
		}
		return emit(out)
	}}
}

// FromFiles returns a readable stream of the given files; nil entries are skipped.
func FromFiles(files ...*vinyl.File) *Stream {
	return &Stream{source: func(emit emitFunc) error {
		for _, f := range files {
			if f == nil {
				continue
			}
			if err := emit(f); err != nil {
				return err
			}
		}
		return nil
	}}
}

// Pipe connects s as the input of next and returns next, like readable.pipe(next). A nil next returns s.
func (s *Stream) Pipe(next *Stream) *Stream {
	if next == nil {
		return s
	}
	next.mu.Lock()
	next.upstream = s
	next.mu.Unlock()
	return next
}

// Wait drains the pipeline ending at s and returns the files it emitted, in order, with the first error. A
// stream that was already drained yields no files.
func (s *Stream) Wait() ([]*vinyl.File, error) {
	if s == nil {
		return nil, errNilStream
	}
	var files []*vinyl.File
	err := s.run(func(f *vinyl.File) error {
		files = append(files, f)
		return nil
	})
	return files, err
}

// run pushes the stage's files into emit: upstream files pass through the transform (or unchanged when the stage
// has none, as to-through passes them), then the stage's own files follow.
func (s *Stream) run(emit emitFunc) error {
	s.mu.Lock()
	if s.consumed {
		s.mu.Unlock()
		return nil
	}
	s.consumed = true
	upstream := s.upstream
	s.mu.Unlock()
	if upstream != nil {
		next := emit
		if s.transform != nil {
			next = func(f *vinyl.File) error { return s.transform(f, emit) }
		}
		if err := upstream.run(next); err != nil {
			return err
		}
	}
	if s.source != nil {
		return s.source(emit)
	}
	return nil
}
