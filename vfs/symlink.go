package vfs

import (
	"errors"

	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vinyl"
)

// symlinkConfig follows vinyl-fs's lib/symlink/options.js.
var symlinkConfig = map[string]optionDef{
	"cwd":              {types: []string{typeString}, def: workingDir},
	"dirMode":          {types: []string{typeNumber}},
	"overwrite":        {types: []string{typeBoolean}, def: true},
	"relativeSymlinks": {types: []string{typeBoolean}, def: false},
	"useJunctions":     {types: []string{typeBoolean}, def: true},
}

// Symlink returns a stream that links files into folder: for every file a symbolic link to it (or to its symlink
// target) is created below folder, a string or a func(*vinyl.File) string, and the file is passed on describing
// the link. Link targets are absolute unless relativeSymlinks is true; overwrite defaults to true.
//
// The returned error is "Invalid symlink() folder argument. Please specify a non-empty string or a function." when
// folder is falsy as JavaScript counts it (nil, "", false, 0, NaN or a nil function), the original's check of the
// argument. A folder that resolves to no string for a file (another type, or a function returning "") fails that
// file with "Invalid output folder"; that error, a file without a path and file system errors with Node.js's texts
// stop the pipeline and come from Wait.
func Symlink(folder any, opts Options) (*Stream, error) {
	if !truthy(folder) {
		return nil, errors.New("Invalid symlink() folder argument. Please specify a non-empty string or a function.")
	}
	r := newResolver(symlinkConfig, opts)
	fr := newResolver(folderConfig, Options{"outFolder": folder})
	return &Stream{transform: func(f *vinyl.File, emit emitFunc) error {
		if err := r.prepareSymlink(fr, f); err != nil {
			return err
		}
		if err := r.mkdirFor(f); err != nil {
			return err
		}
		if err := r.linkFile(f); err != nil {
			return err
		}
		return emit(f)
	}}, nil
}

// prepareSymlink follows lib/symlink/prepare.js: the file becomes a link at its new path pointing to its old path
// (or to the symlink already set), with null contents.
func (r *resolver) prepareSymlink(fr *resolver, f *vinyl.File) error {
	cwd, base, writePath, err := r.outputPaths(fr, f)
	if err != nil {
		return err
	}
	if f.Stat == nil {
		f.Stat = &vinyl.Stat{}
	}
	if err := f.SetCwd(cwd); err != nil {
		return err
	}
	if err := f.SetBase(base); err != nil {
		return err
	}
	if f.Symlink() == "" {
		f.SetSymlink(f.Path())
	}
	if err := f.SetPath(writePath); err != nil {
		return err
	}
	f.SetContents(nil)
	return nil
}

// linkFile follows lib/symlink/link-file.js on posix.
func (r *resolver) linkFile(f *vinyl.File) error {
	if r.boolean("relativeSymlinks", f) {
		target, err := nodepath.Relative(f.Base(), f.Symlink())
		if err != nil {
			return err
		}
		f.SetSymlink(target)
	}
	_, exclusive, flagsName := r.openFlags(f, false)
	if err := createSymlink(f.Symlink(), f.Path(), flagsName, exclusive); err != nil {
		return err
	}
	st, err := lstatFile(f.Path())
	if err != nil {
		return err
	}
	f.Stat = st
	return nil
}
