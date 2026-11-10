package vfs

import (
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vinyl"
)

// FSError is a file system error worded the way Node.js words it, for example
// "ENOENT: no such file or directory, open '/x'". It unwraps to the syscall.Errno, so errors.Is works with the
// io/fs sentinel errors.
type FSError struct {
	Code    string        // error code such as "ENOENT"
	Syscall string        // the Node.js syscall name such as "open", "scandir" or "symlink"
	Path    string        // the path argument, "" when Node.js reports none
	Dest    string        // the second path of link calls, "" otherwise
	Errno   syscall.Errno // the underlying error number
	desc    string
}

// Error implements error with Node's uvException text.
func (e *FSError) Error() string {
	var b strings.Builder
	b.WriteString(e.Code + ": " + e.desc + ", " + e.Syscall)
	if e.Path != "" {
		b.WriteString(" '" + e.Path + "'")
	}
	if e.Dest != "" {
		b.WriteString(" -> '" + e.Dest + "'")
	}
	return b.String()
}

// Unwrap returns the errno.
func (e *FSError) Unwrap() error { return e.Errno }

// uvErrors holds the libuv code and message of the errors file operations report.
var uvErrors = map[syscall.Errno][2]string{
	syscall.E2BIG:        {"E2BIG", "argument list too long"},
	syscall.EACCES:       {"EACCES", "permission denied"},
	syscall.EAGAIN:       {"EAGAIN", "resource temporarily unavailable"},
	syscall.EBADF:        {"EBADF", "bad file descriptor"},
	syscall.EBUSY:        {"EBUSY", "resource busy or locked"},
	syscall.EEXIST:       {"EEXIST", "file already exists"},
	syscall.EFAULT:       {"EFAULT", "bad address in system call argument"},
	syscall.EFBIG:        {"EFBIG", "file too large"},
	syscall.EINTR:        {"EINTR", "interrupted system call"},
	syscall.EINVAL:       {"EINVAL", "invalid argument"},
	syscall.EIO:          {"EIO", "i/o error"},
	syscall.EISDIR:       {"EISDIR", "illegal operation on a directory"},
	syscall.ELOOP:        {"ELOOP", "too many symbolic links encountered"},
	syscall.EMFILE:       {"EMFILE", "too many open files"},
	syscall.EMLINK:       {"EMLINK", "too many links"},
	syscall.ENAMETOOLONG: {"ENAMETOOLONG", "name too long"},
	syscall.ENFILE:       {"ENFILE", "file table overflow"},
	syscall.ENODEV:       {"ENODEV", "no such device"},
	syscall.ENOENT:       {"ENOENT", "no such file or directory"},
	syscall.ENOMEM:       {"ENOMEM", "not enough memory"},
	syscall.ENOSPC:       {"ENOSPC", "no space left on device"},
	syscall.ENOSYS:       {"ENOSYS", "function not implemented"},
	syscall.ENOTDIR:      {"ENOTDIR", "not a directory"},
	syscall.ENOTEMPTY:    {"ENOTEMPTY", "directory not empty"},
	syscall.ENOTSUP:      {"ENOTSUP", "operation not supported on socket"},
	syscall.ENXIO:        {"ENXIO", "no such device or address"},
	syscall.EPERM:        {"EPERM", "operation not permitted"},
	syscall.EROFS:        {"EROFS", "read-only file system"},
	syscall.ESPIPE:       {"ESPIPE", "invalid seek"},
	syscall.ETXTBSY:      {"ETXTBSY", "text file is busy"},
	syscall.EXDEV:        {"EXDEV", "cross-device link not permitted"},
}

// nodeErr rewords err, when it carries an errno, as Node.js reports a failed call of syscall on path.
func nodeErr(call, path string, err error) error {
	return nodeLinkErr(call, path, "", err)
}

// nodeLinkErr is nodeErr for calls with two paths (symlink, link, rename).
func nodeLinkErr(call, path, dest string, err error) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	e := &FSError{Code: "UNKNOWN", desc: "unknown error", Syscall: call, Path: path, Dest: dest, Errno: errno}
	if uv, ok := uvErrors[errno]; ok {
		e.Code, e.desc = uv[0], uv[1]
	}
	return e
}

// hasErrno reports whether err carries the given errno.
func hasErrno(err error, errno syscall.Errno) bool {
	return errors.Is(err, errno)
}

// lstatFile is fs.lstat as a vinyl stat.
func lstatFile(p string) (*vinyl.Stat, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return nil, nodeErr("lstat", p, err)
	}
	return statFromSys(&st), nil
}

// statFile is fs.stat as a vinyl stat.
func statFile(p string) (*vinyl.Stat, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return nil, nodeErr("stat", p, err)
	}
	return statFromSys(&st), nil
}

// readlink is fs.readlink.
func readlink(p string) (string, error) {
	target, err := os.Readlink(p)
	if err != nil {
		return "", nodeErr("readlink", p, err)
	}
	return target, nil
}

// readFile is fs.readFile: open errors name the path, read errors do not.
func readFile(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nodeErr("open", p, err)
	}
	data, rerr := io.ReadAll(f)
	cerr := f.Close()
	if rerr != nil {
		return nil, nodeErr("read", "", rerr)
	}
	if cerr != nil {
		return nil, nodeErr("close", "", cerr)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

// realpath follows Node's JavaScript fs.realpath on posix: every component is lstat'ed, links are stat'ed before
// they are read (which reports ELOOP and dangling links) and resolution restarts from each link target. Every
// file system call advances clock.
func realpath(p string, clock *stepClock) (string, error) {
	p, err := nodepath.Resolve(p)
	if err != nil {
		return "", err
	}
	seenLinks := map[[2]uint64]string{}
	knownHard := map[string]bool{}
	current, base := "/", "/"
	pos := 1
	for pos < len(p) {
		previous := current
		next := strings.IndexByte(p[pos:], '/')
		if next == -1 {
			current += p[pos:]
			base = previous + p[pos:]
			pos = len(p)
		} else {
			next += pos
			current += p[pos : next+1]
			base = previous + p[pos:next]
			pos = next + 1
		}
		if knownHard[base] {
			continue
		}
		var st syscall.Stat_t
		clock.step()
		if err := syscall.Lstat(base, &st); err != nil {
			return "", nodeErr("lstat", base, err)
		}
		if uint32(st.Mode)&syscall.S_IFMT != syscall.S_IFLNK {
			knownHard[base] = true
			continue
		}
		id := [2]uint64{uint64(st.Dev), st.Ino}
		target, seen := seenLinks[id]
		if !seen {
			var tst syscall.Stat_t
			clock.step()
			if err := syscall.Stat(base, &tst); err != nil {
				return "", nodeErr("stat", base, err)
			}
			clock.step()
			t, err := readlink(base)
			if err != nil {
				return "", err
			}
			target = t
			seenLinks[id] = target
		}
		resolvedTarget, err := nodepath.Resolve(previous, target)
		if err != nil {
			return "", err
		}
		if p, err = nodepath.Resolve(resolvedTarget, p[pos:]); err != nil {
			return "", err
		}
		current, base = "/", "/"
		pos = 1
	}
	return p, nil
}

// stepClock counts asynchronous file system calls. The original runs the glob walker and the per-file stages
// concurrently, so when both fail the error that needs fewer calls is the one that destroys the stream; Src
// replays that race by counting one step per call on each side.
type stepClock struct{ now int }

// step records one file system call.
func (c *stepClock) step() { c.now++ }

// isOwner is file-operations isOwner: the effective user owns the file or is root.
func isOwner(uid uint32) bool {
	euid := os.Geteuid()
	if euid < 0 {
		return false
	}
	return uid == uint32(euid) || euid == 0
}

// msTime converts t to the millisecond Date a Node.js Stats object holds: new Date(ms + 0.5), which truncates
// toward zero.
func msTime(t time.Time) time.Time {
	half := int64(time.Millisecond) / 2
	return time.UnixMilli((t.UnixNano() + half) / int64(time.Millisecond))
}
