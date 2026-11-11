package vfs

import (
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vinyl"
)

// maskMode follows vinyl-fs's MASK_MODE.
const maskMode = 0o7777

// defaultFileMode follows vinyl-fs's DEFAULT_FILE_MODE.
const defaultFileMode = 0o666

// destConfig follows vinyl-fs's lib/dest/options.js.
var destConfig = map[string]optionDef{
	"cwd":              {types: []string{typeString}, def: workingDir},
	"mode":             {types: []string{typeNumber}, def: statMode},
	"dirMode":          {types: []string{typeNumber}},
	"overwrite":        {types: []string{typeBoolean}, def: true},
	"append":           {types: []string{typeBoolean}, def: false},
	"encoding":         {types: []string{typeString, typeBoolean}, def: defaultEncoding},
	"sourcemaps":       {types: []string{typeString, typeBoolean}, def: false},
	"relativeSymlinks": {types: []string{typeBoolean}, def: false},
	"useJunctions":     {types: []string{typeBoolean}, def: true},
}

// statMode is the mode option's default: the file's stat mode, undefined without one. A zero mode stands for
// the undefined mode of a stat object built without one.
func statMode(f *vinyl.File) any {
	if f.Stat == nil || f.Stat.Mode == 0 {
		return nil
	}
	return float64(f.Stat.Mode)
}

// folderConfig is the resolve-options config of the output folder argument.
var folderConfig = map[string]optionDef{"outFolder": {types: []string{typeString}}}

// errInvalidOutputFolder is reported when the folder argument resolves to nothing for a file.
var errInvalidOutputFolder = errors.New("Invalid output folder")

// fileMode is the mode a file is written with: set is false for the original's undefined or null.
type fileMode struct {
	set   bool
	value uint32
}

// Dest returns a stream that writes files below folder: folder is a string or a func(*vinyl.File) string. Every
// file is written below the folder and passed on with its cwd, base, path and stat updated; with sourcemaps set to
// a path string the external map files are passed on too. Options default to cwd = the working directory, mode =
// the file's own mode, overwrite = true, append = false, encoding = "utf8" and sourcemaps = false.
//
// The returned error is "Invalid dest() folder argument. Please specify a non-empty string or a function." when
// folder is neither. Errors met while writing (an invalid output folder, a file without a path, file system
// errors with Node.js's texts) stop the pipeline and come from Wait.
func Dest(folder any, opts Options) (*Stream, error) {
	if !truthy(folder) {
		return nil, errors.New("Invalid dest() folder argument. Please specify a non-empty string or a function.")
	}
	r := newResolver(destConfig, opts)
	fr := newResolver(folderConfig, Options{"outFolder": folder})
	return &Stream{transform: func(f *vinyl.File, emit emitFunc) error {
		mode, err := r.prepareWrite(fr, f)
		if err != nil {
			return err
		}
		files := []*vinyl.File{f}
		modes := []fileMode{mode}
		if sm := r.resolve("sourcemaps", f); truthy(sm) {
			loc, external := sm.(string)
			mapFile, err := writeSourceMap(f, loc, external)
			if err != nil {
				return err
			}
			if mapFile != nil {
				files = append(files, mapFile)
				modes = append(modes, fileMode{})
			}
		}
		for i, out := range files {
			if err := r.mkdirFor(out); err != nil {
				return err
			}
			if err := r.writeContents(out, modes[i]); err != nil {
				return err
			}
			if err := emit(out); err != nil {
				return err
			}
		}
		return nil
	}}, nil
}

// prepareWrite follows lib/dest/prepare.js: the file moves below the output folder and gets the mode option.
func (r *resolver) prepareWrite(fr *resolver, f *vinyl.File) (fileMode, error) {
	cwd, base, writePath, err := r.outputPaths(fr, f)
	if err != nil {
		return fileMode{}, err
	}
	if err := f.SetCwd(cwd); err != nil {
		return fileMode{}, err
	}
	if err := f.SetBase(base); err != nil {
		return fileMode{}, err
	}
	if err := f.SetPath(writePath); err != nil {
		return fileMode{}, err
	}
	var mode fileMode
	if !f.IsSymbolic() {
		if n, ok := r.resolve("mode", f).(float64); ok {
			mode = fileMode{set: true, value: uint32(int64(n))}
		}
		if f.Stat == nil {
			f.Stat = &vinyl.Stat{}
		}
		f.Stat.Mode = mode.value
	}
	return mode, nil
}

// outputPaths resolves the cwd option, the output folder and the path the file is written to.
func (r *resolver) outputPaths(fr *resolver, f *vinyl.File) (cwd, base, writePath string, err error) {
	folder, _ := fr.resolve("outFolder", f).(string)
	if folder == "" {
		return "", "", "", errInvalidOutputFolder
	}
	cwdOpt, _ := r.resolve("cwd", f).(string)
	if cwd, err = nodepath.Resolve(cwdOpt); err != nil {
		return "", "", "", err
	}
	if base, err = nodepath.Resolve(cwd, folder); err != nil {
		return "", "", "", err
	}
	rel, err := f.Relative()
	if err != nil {
		return "", "", "", err
	}
	if writePath, err = nodepath.Resolve(base, rel); err != nil {
		return "", "", "", err
	}
	return cwd, base, writePath, nil
}

// mkdirFor follows fs-mkdirp-stream with the dirpath resolver of dest and symlink.
func (r *resolver) mkdirFor(f *vinyl.File) error {
	dirname, err := f.Dirname()
	if err != nil {
		return err
	}
	return mkdirp(dirname, r.dirMode(f))
}

// dirMode resolves the dirMode option; zero is the original's falsy (unset) mode.
func (r *resolver) dirMode(f *vinyl.File) uint32 {
	n, _ := r.resolve("dirMode", f).(float64)
	return uint32(int64(n))
}

// openFlags is file-operations getFlags as open(2) flags, with whether the x (exclusive) flag is set.
func (r *resolver) openFlags(f *vinyl.File, appendOpt bool) (flags int, exclusive bool, flagsName string) {
	overwrite := r.boolean("overwrite", f)
	flagsName = "w"
	flags = syscall.O_WRONLY | syscall.O_CREAT | syscall.O_TRUNC
	if appendOpt {
		flagsName = "a"
		flags = syscall.O_WRONLY | syscall.O_CREAT | syscall.O_APPEND
	}
	if !overwrite {
		flagsName += "x"
		flags |= syscall.O_EXCL
	}
	return flags | syscall.O_CLOEXEC, !overwrite, flagsName
}

// isFatalOverwriteError is file-operations isFatalOverwriteError.
func isFatalOverwriteError(err error, exclusive bool) bool {
	if err == nil {
		return false
	}
	return !(hasErrno(err, syscall.EEXIST) && exclusive)
}

// writeContents follows lib/dest/write-contents.
func (r *resolver) writeContents(f *vinyl.File, mode fileMode) error {
	var err error
	switch {
	case f.IsSymbolic():
		err = r.writeSymbolicLink(f)
	case f.IsDirectory():
		err = writeDir(f, mode)
	case f.IsStream():
		err = r.writeStream(f, mode)
	case f.IsBuffer():
		err = r.writeBuffer(f, mode)
	}
	_, exclusive, _ := r.openFlags(f, r.boolean("append", f))
	if isFatalOverwriteError(err, exclusive) {
		return err
	}
	return nil
}

// openForWrite opens path the way fs.open(path, flags, mode || 0o666) does.
func openForWrite(path string, flags int, mode fileMode) (*os.File, error) {
	perm := uint32(defaultFileMode)
	if mode.set && mode.value != 0 {
		perm = mode.value & maskMode
	}
	fd, err := syscall.Open(path, flags, perm)
	if err != nil {
		return nil, nodeErr("open", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

// writeBuffer follows lib/dest/write-contents/write-buffer.js.
func (r *resolver) writeBuffer(f *vinyl.File, mode fileMode) error {
	flags, _, _ := r.openFlags(f, r.boolean("append", f))
	enc, c, err := r.codecFor(f)
	if err != nil {
		return err
	}
	contents := f.Contents()
	if enc != "" && c.enc != defaultEncoding {
		if contents, err = c.encodeBuffer(decodeUTF8(contents)); err != nil {
			return err
		}
	}
	out, err := openForWrite(f.Path(), flags, mode)
	if err != nil {
		return err
	}
	if _, err := out.Write(contents); err != nil {
		return closeWith(out, nodeErr("write", "", err))
	}
	return closeWith(out, updateMetadata(out, f, mode))
}

// closeWith is file-operations closeFd: the first error wins.
func closeWith(out *os.File, err error) error {
	cerr := out.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return nodeErr("close", "", cerr)
	}
	return nil
}

// writeStream follows lib/dest/write-contents/write-stream.js: the contents are streamed into the file, then the
// file's contents become a fresh stream of what was written.
func (r *resolver) writeStream(f *vinyl.File, mode fileMode) error {
	flags, _, _ := r.openFlags(f, r.boolean("append", f))
	enc, c, err := r.codecFor(f)
	if err != nil {
		return err
	}
	out, err := openForWrite(f.Path(), flags, mode)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(f.ContentsStream())
	if err != nil {
		return closeWith(out, err)
	}
	if enc != "" && c.enc != defaultEncoding {
		data = c.encodeStream(decodeUTF8(data))
	}
	if _, err := out.Write(data); err != nil {
		return closeWith(out, nodeErr("write", "", err))
	}
	f.SetContentsStream(newFileStream(f.Path(), enc, c, false))
	return closeWith(out, updateMetadata(out, f, mode))
}

// writeDir follows lib/dest/write-contents/write-dir.js.
func writeDir(f *vinyl.File, mode fileMode) error {
	if err := mkdirp(f.Path(), mode.value); err != nil {
		return err
	}
	dir, err := os.Open(f.Path())
	if err != nil {
		if hasErrno(err, syscall.EACCES) {
			return nil
		}
		return nodeErr("open", f.Path(), err)
	}
	return closeWith(dir, updateMetadata(dir, f, mode))
}

// updateMetadata is file-operations updateMetadata: the file's stat becomes the on-disk stat, then mode and
// times are corrected when the vinyl stat asks for different ones and the process owns the file.
func updateMetadata(fd *os.File, f *vinyl.File, mode fileMode) error {
	var sys syscall.Stat_t
	if err := syscall.Fstat(int(fd.Fd()), &sys); err != nil {
		return nodeErr("fstat", "", err)
	}
	fsStat := statFromSys(&sys)
	vinylStat := *f.Stat

	var modeDiff uint32
	if mode.set {
		modeDiff = (mode.value ^ fsStat.Mode) & maskMode
	}
	setTimes := false
	var atime = vinylStat.Atime
	if !vinylStat.Mtime.IsZero() {
		sameM := vinylStat.Mtime.UnixMilli() == fsStat.Mtime.UnixMilli()
		sameA := !atime.IsZero() && atime.UnixMilli() == fsStat.Atime.UnixMilli()
		if !(sameM && sameA) {
			setTimes = true
			if atime.IsZero() {
				atime = fsStat.Atime
			}
		}
	}

	*f.Stat = *fsStat
	if vinylStat.Mtime.IsZero() {
		// The original reads the missing mtime of a fresh stat object before copying the disk stat into it,
		// which leaves its mtime an invalid Date; the zero time stands for that.
		f.Stat.Mtime = time.Time{}
	}
	if modeDiff == 0 && !setTimes {
		return nil
	}
	if !isOwner(sys.Uid) {
		return nil
	}
	var firstErr error
	if modeDiff != 0 {
		newMode := fsStat.Mode ^ modeDiff
		if err := syscall.Fchmod(int(fd.Fd()), newMode&maskMode); err != nil {
			firstErr = nodeErr("fchmod", "", err)
		} else {
			f.Stat.Mode = newMode
		}
	}
	if setTimes {
		tv := []syscall.Timeval{
			syscall.NsecToTimeval(atime.UnixNano()),
			syscall.NsecToTimeval(vinylStat.Mtime.UnixNano()),
		}
		err := syscall.Futimes(int(fd.Fd()), tv)
		switch {
		case err == nil:
			f.Stat.Atime = atime
			f.Stat.Mtime = vinylStat.Mtime
		case hasErrno(err, syscall.ENOSYS):
			f.Stat.Atime = fsStat.Atime
			f.Stat.Mtime = fsStat.Mtime
		case firstErr == nil:
			firstErr = nodeErr("futime", "", err)
		}
	}
	return firstErr
}

// writeSymbolicLink follows lib/dest/write-contents/write-symbolic-link.js on posix.
func (r *resolver) writeSymbolicLink(f *vinyl.File) error {
	if f.Symlink() == "" {
		return errors.New("Missing symlink property on symbolic vinyl")
	}
	isRelative := r.boolean("relativeSymlinks", f)
	_, exclusive, flagsName := r.openFlags(f, r.boolean("append", f))
	if isRelative {
		target, err := nodepath.Relative(f.Base(), f.Symlink())
		if err != nil {
			return err
		}
		f.SetSymlink(target)
	}
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

// createSymlink is file-operations symlink: with the plain "w" flag an existing entry is unlinked first, and an
// EEXIST failure is ignored when the exclusive flag is set.
func createSymlink(target, path, flagsName string, exclusive bool) error {
	if flagsName == "w" {
		if err := syscall.Unlink(path); err != nil && !hasErrno(err, syscall.ENOENT) {
			return nodeErr("unlink", path, err)
		}
	}
	if err := syscall.Symlink(target, path); err != nil {
		lerr := nodeLinkErr("symlink", target, path, err)
		if isFatalOverwriteError(lerr, exclusive) {
			return lerr
		}
	}
	return nil
}

// mkdirp follows fs-mkdirp-stream 2.0.1's mkdirp; mode 0 is the original's undefined mode.
func mkdirp(dirpath string, mode uint32) error {
	dirpath, err := nodepath.Resolve(dirpath)
	if err != nil {
		return err
	}
	perm := uint32(0o777)
	if mode != 0 {
		perm = mode & maskMode
	}
	mkErr := syscall.Mkdir(dirpath, perm)
	if mkErr == nil {
		return mkdirpCheck(dirpath, mode, nil)
	}
	switch {
	case hasErrno(mkErr, syscall.ENOENT):
		if err := mkdirp(nodepath.Dirname(dirpath), 0); err != nil {
			return err
		}
		return mkdirp(dirpath, mode)
	case hasErrno(mkErr, syscall.EEXIST):
		return mkdirpCheck(dirpath, mode, mkErr)
	case hasErrno(mkErr, syscall.ENOTDIR):
		// The nearest ancestor that exists decides the error; the missing or non-folder parts below it fail stat
		// with ENOENT or ENOTDIR, any other stat error is the result.
		p := dirpath
		for {
			_, err := statFile(p)
			if err == nil {
				return mkdirpCheck(p, mode, mkErr)
			}
			if !hasErrno(err, syscall.ENOENT) && !hasErrno(err, syscall.ENOTDIR) {
				return err
			}
			p = nodepath.Dirname(p)
		}
	}
	return nodeErr("mkdir", dirpath, mkErr)
}

// mkdirpCheck is mkdirp's onStat: the path must be a directory (or a link to one) and gets the mode when one is
// given and differs.
func mkdirpCheck(dirpath string, mode uint32, mkErr error) error {
	st, err := statFile(dirpath)
	if err != nil {
		if !hasErrno(err, syscall.ENOENT) {
			return err
		}
		if st, err = lstatFile(dirpath); err != nil {
			return err
		}
	}
	if !st.IsDirectory() {
		return mkdirpNonDirectory(dirpath, mkErr)
	}
	if mode == 0 || st.Mode&maskMode == mode {
		return nil
	}
	if err := syscall.Chmod(dirpath, mode&maskMode); err != nil {
		return nodeErr("chmod", dirpath, err)
	}
	return nil
}

// mkdirpNonDirectory is mkdirp's onNonDirectory: reading the entry (or the link target) as a directory surfaces
// the ENOTDIR error the original reports. mkErr is the mkdir error that led here and is never nil; when the entry
// cannot be lstat'ed or its link read, that mkdir error is returned, as fs-mkdirp-stream bubbles it.
func mkdirpNonDirectory(dirpath string, mkErr error) error {
	st, err := lstatFile(dirpath)
	if err != nil {
		return nodeErr("mkdir", dirpath, mkErr)
	}
	target := dirpath
	if st.IsSymbolicLink() {
		link, err := readlink(dirpath)
		if err != nil {
			return nodeErr("mkdir", dirpath, mkErr)
		}
		target = link
	}
	if _, err := os.ReadDir(target); err != nil {
		return nodeErr("scandir", target, err)
	}
	return nil
}
