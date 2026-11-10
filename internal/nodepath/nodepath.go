// Package nodepath reproduces the posix branch of Node.js's path module (normalize, join, resolve, relative,
// dirname, basename and extname) byte for byte, so vinyl and vfs compute the same paths as Node.js
// programs do. Go's path.Clean differs from path.normalize on trailing separators, empty input and
// leading ".." segments, which is why they are reimplemented rather than wrapped.
package nodepath

import (
	"os"
	"strings"
	"syscall"
)

// Cwd returns the process working directory from the getcwd system call, which reports the physical directory
// as Node.js does. The error is getcwd's, for example when the working directory has been removed (Node.js's
// process.cwd() throws then).
func Cwd() (string, error) {
	if wd, err := syscall.Getwd(); err == nil {
		return wd, nil
	}
	return os.Getwd()
}

// IsAbs reports whether p starts with a slash (Node's path.posix.isAbsolute).
func IsAbs(p string) bool {
	return strings.HasPrefix(p, "/")
}

// normalizeString resolves "." and ".." segments the way Node's internal normalizeString does.
func normalizeString(p string, allowAboveRoot bool) string {
	res := ""
	lastSegmentLength := 0
	lastSlash := -1
	dots := 0
	var code byte
	for i := 0; i <= len(p); i++ {
		if i < len(p) {
			code = p[i]
		} else if code == '/' {
			break
		} else {
			code = '/'
		}
		if code == '/' {
			if lastSlash == i-1 || dots == 1 {
				// Empty segment or ".": nothing to add.
			} else if dots == 2 {
				if len(res) < 2 || lastSegmentLength != 2 || !strings.HasSuffix(res, "..") {
					if len(res) > 2 {
						idx := strings.LastIndexByte(res, '/')
						if idx == -1 {
							res = ""
							lastSegmentLength = 0
						} else {
							res = res[:idx]
							lastSegmentLength = len(res) - 1 - strings.LastIndexByte(res, '/')
						}
						lastSlash = i
						dots = 0
						continue
					} else if len(res) != 0 {
						res = ""
						lastSegmentLength = 0
						lastSlash = i
						dots = 0
						continue
					}
				}
				if allowAboveRoot {
					if len(res) > 0 {
						res += "/.."
					} else {
						res = ".."
					}
					lastSegmentLength = 2
				}
			} else {
				if len(res) > 0 {
					res += "/" + p[lastSlash+1:i]
				} else {
					res = p[lastSlash+1 : i]
				}
				lastSegmentLength = i - lastSlash - 1
			}
			lastSlash = i
			dots = 0
		} else if code == '.' && dots != -1 {
			dots++
		} else {
			dots = -1
		}
	}
	return res
}

// Normalize resolves "." and ".." segments and repeated slashes, keeping a trailing slash and returning "."
// for an empty path (Node's path.posix.normalize).
func Normalize(p string) string {
	if p == "" {
		return "."
	}
	isAbs := p[0] == '/'
	trailing := p[len(p)-1] == '/'
	p = normalizeString(p, !isAbs)
	if p == "" {
		if isAbs {
			return "/"
		}
		if trailing {
			return "./"
		}
		return "."
	}
	if trailing {
		p += "/"
	}
	if isAbs {
		return "/" + p
	}
	return p
}

// Join joins the non-empty segments with slashes and normalizes the result (Node's path.posix.join).
func Join(parts ...string) string {
	joined := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if joined == "" {
			joined = part
		} else {
			joined += "/" + part
		}
	}
	if joined == "" {
		return "."
	}
	return Normalize(joined)
}

// Resolve returns an absolute normalized path (Node's path.posix.resolve): segments are applied right to left until
// one is absolute, then the process working directory is prepended. The working directory is read only when no
// segment is absolute; its error (the directory was removed) is returned.
func Resolve(parts ...string) (string, error) {
	return resolveWith(Cwd, parts)
}

// ResolveIn is Resolve with dir standing for the working directory, which is then never read; dir must be
// absolute.
func ResolveIn(dir string, parts ...string) string {
	p, _ := resolveWith(func() (string, error) { return dir, nil }, parts)
	return p
}

// RelativeIn is Relative with dir standing for the working directory, which is then never read; dir must be
// absolute.
func RelativeIn(dir, from, to string) string {
	p, _ := relativeWith(func() (string, error) { return dir, nil }, from, to)
	return p
}

// resolveWith implements Resolve with cwd supplying the working directory.
func resolveWith(cwd func() (string, error), parts []string) (string, error) {
	resolved := ""
	absolute := false
	for i := len(parts) - 1; i >= -1 && !absolute; i-- {
		var p string
		if i >= 0 {
			p = parts[i]
		} else {
			wd, err := cwd()
			if err != nil {
				return "", err
			}
			p = wd
		}
		if p == "" {
			continue
		}
		resolved = p + "/" + resolved
		absolute = p[0] == '/'
	}
	resolved = normalizeString(resolved, !absolute)
	if absolute {
		return "/" + resolved, nil
	}
	if resolved == "" {
		return ".", nil
	}
	return resolved, nil
}

// Relative returns the path from from to to after resolving both (Node's path.posix.relative), or the error of
// reading the working directory when one of them is relative and it cannot be read.
func Relative(from, to string) (string, error) {
	return relativeWith(Cwd, from, to)
}

// relativeWith implements Relative with cwd supplying the working directory.
func relativeWith(cwd func() (string, error), from, to string) (string, error) {
	if from == to {
		return "", nil
	}
	from, err := resolveWith(cwd, []string{from})
	if err != nil {
		return "", err
	}
	if to, err = resolveWith(cwd, []string{to}); err != nil {
		return "", err
	}
	if from == to {
		return "", nil
	}
	const fromStart, toStart = 1, 1
	fromEnd := len(from)
	fromLen := fromEnd - fromStart
	toLen := len(to) - toStart
	length := min(fromLen, toLen)
	lastCommonSep := -1
	i := 0
	for ; i < length; i++ {
		c := from[fromStart+i]
		if c != to[toStart+i] {
			break
		}
		if c == '/' {
			lastCommonSep = i
		}
	}
	if i == length {
		if toLen > length {
			if to[toStart+i] == '/' {
				return to[toStart+i+1:], nil
			}
			if i == 0 {
				return to[toStart+i:], nil
			}
		} else if fromLen > length {
			if from[fromStart+i] == '/' {
				lastCommonSep = i
			} else if i == 0 {
				lastCommonSep = 0
			}
		}
	}
	out := ""
	for i = fromStart + lastCommonSep + 1; i <= fromEnd; i++ {
		if i == fromEnd || from[i] == '/' {
			if out == "" {
				out = ".."
			} else {
				out += "/.."
			}
		}
	}
	return out + to[toStart+lastCommonSep:], nil
}

// Dirname returns p without its last segment (Node's path.posix.dirname).
func Dirname(p string) string {
	if p == "" {
		return "."
	}
	hasRoot := p[0] == '/'
	end := -1
	matchedSlash := true
	for i := len(p) - 1; i >= 1; i-- {
		if p[i] == '/' {
			if !matchedSlash {
				end = i
				break
			}
		} else {
			matchedSlash = false
		}
	}
	if end == -1 {
		if hasRoot {
			return "/"
		}
		return "."
	}
	if hasRoot && end == 1 {
		return "//"
	}
	return p[:end]
}

// Basename returns the last segment of p (Node's path.posix.basename); a non-empty suffix is removed from the result as Node removes it.
func Basename(p, suffix string) string {
	start := 0
	end := -1
	matchedSlash := true
	if suffix != "" && len(suffix) <= len(p) {
		if suffix == p {
			return ""
		}
		extIdx := len(suffix) - 1
		firstNonSlashEnd := -1
		for i := len(p) - 1; i >= 0; i-- {
			c := p[i]
			if c == '/' {
				if !matchedSlash {
					start = i + 1
					break
				}
				continue
			}
			if firstNonSlashEnd == -1 {
				matchedSlash = false
				firstNonSlashEnd = i + 1
			}
			if extIdx >= 0 {
				if c == suffix[extIdx] {
					extIdx--
					if extIdx == -1 {
						end = i
					}
				} else {
					extIdx = -1
					end = firstNonSlashEnd
				}
			}
		}
		if start == end {
			end = firstNonSlashEnd
		} else if end == -1 {
			end = len(p)
		}
		return p[start:end]
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if !matchedSlash {
				start = i + 1
				break
			}
		} else if end == -1 {
			matchedSlash = false
			end = i + 1
		}
	}
	if end == -1 {
		return ""
	}
	return p[start:end]
}

// Extname returns the extension of the last segment of p, from its last dot (Node's path.posix.extname).
func Extname(p string) string {
	startDot := -1
	startPart := 0
	end := -1
	matchedSlash := true
	preDotState := 0
	for i := len(p) - 1; i >= 0; i-- {
		c := p[i]
		if c == '/' {
			if !matchedSlash {
				startPart = i + 1
				break
			}
			continue
		}
		if end == -1 {
			matchedSlash = false
			end = i + 1
		}
		if c == '.' {
			if startDot == -1 {
				startDot = i
			} else if preDotState != 1 {
				preDotState = 1
			}
		} else if startDot != -1 {
			preDotState = -1
		}
	}
	if startDot == -1 || end == -1 || preDotState == 0 ||
		(preDotState == 1 && startDot == end-1 && startDot == startPart+1) {
		return ""
	}
	return p[startDot:end]
}
