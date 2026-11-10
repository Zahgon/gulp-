package vfs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/gulpjs/gulp/internal/glob"
	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vinyl"
)

// This file walks globs as glob-stream 8.0.4 does: globs are made absolute, the glob-parent directory of every
// positive glob is walked (a non-glob path is only checked for existence), each path is matched with anymatch, and
// singular globs that matched nothing fail the stream unless allowEmpty is set.

const (
	globErrMessage1 = "File not found with singular glob: "
	globErrMessage2 = " (if this was purposeful, use `allowEmpty` option)"
)

// globEntry is the {cwd, base, path} object glob-stream emits, with the walker step it was emitted at.
type globEntry struct {
	cwd, base, path string
	at              int
}

// walkError is a walker failure and the walker step it happened at.
type walkError struct {
	err error
	at  int
}

// undefinedKey stands for JavaScript's undefined in the uniqueBy set.
type undefinedKey struct{}

// globStream is a configured glob-stream.
type globStream struct {
	cwd           string
	base          string
	allowEmpty    bool
	uniqueBy      any
	positiveGlobs []string
	matcher       *glob.Matcher
	positiveIndex []int // original glob index -> index among positive globs
}

// optionalBool reads a boolean glob-stream option: absent means def, any present non-bool value is invalid.
func optionalBool(opts Options, key string, def bool) (bool, error) {
	v, present := opts[key]
	if !present {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("The `%s` option must be a boolean", key)
	}
	return b, nil
}

// newGlobStream validates the globs and options exactly as glob-stream does when it is called.
func newGlobStream(globs []string, opts Options) (*globStream, error) {
	hasPositive := false
	for _, g := range globs {
		if _, negated := glob.IsNegated(g); !negated {
			hasPositive = true
		}
	}
	if !hasPositive {
		return nil, errors.New("Missing positive glob")
	}

	wd, err := nodepath.Cwd()
	if err != nil {
		return nil, err
	}
	cwd := wd
	if v, present := opts["cwd"]; present {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("The `cwd` option must be a string")
		}
		cwd = s
	}
	if _, err := optionalBool(opts, "dot", false); err != nil {
		return nil, err
	}
	cwdbase, err := optionalBool(opts, "cwdbase", false)
	if err != nil {
		return nil, err
	}
	var uniqueBy any = "path"
	if v, present := opts["uniqueBy"]; present {
		if _, ok := v.(string); !ok && !isFunction(v) {
			return nil, errors.New("The `uniqueBy` option must be a string or function")
		}
		uniqueBy = v
	}
	allowEmpty, err := optionalBool(opts, "allowEmpty", false)
	if err != nil {
		return nil, err
	}
	base := ""
	if v := opts["base"]; truthy(v) {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("The `base` option must be a string if specified")
		}
		base = s
	}
	var ignore []any
	if v, present := opts["ignore"]; present {
		switch t := v.(type) {
		case string:
			ignore = []any{t}
		case []string:
			for _, s := range t {
				ignore = append(ignore, s)
			}
		case []any:
			ignore = t
		default:
			return nil, errors.New("The `ignore` option must be a string or array")
		}
	}

	absCwd := nodepath.ResolveIn(wd, cwd)
	gs := &globStream{
		cwd:        glob.NormalizePath(absCwd, true),
		allowEmpty: allowEmpty,
		uniqueBy:   uniqueBy,
	}
	if cwdbase {
		gs.base = gs.cwd
	} else {
		gs.base = base
	}

	root, err := rootOption(opts["root"])
	if err != nil {
		return nil, err
	}
	abs := make([]string, len(globs))
	for i, g := range globs {
		abs[i] = glob.ToAbsolute(g, gs.cwd, root, wd)
	}
	for i, g := range abs {
		gs.positiveIndex = append(gs.positiveIndex, len(gs.positiveGlobs))
		if !strings.HasPrefix(g, "!") {
			gs.positiveGlobs = append(gs.positiveGlobs, g)
		} else {
			gs.positiveIndex[i] = -1
		}
	}
	matchOpts := glob.Options{
		Dot:        truthy(opts["dot"]),
		NoBrace:    isTrue(opts["nobrace"]),
		NoGlobstar: isTrue(opts["noglobstar"]),
		NoExt:      noExtglob(opts),
		NoCase:     truthy(opts["nocase"]),
		MatchBase:  isTrue(opts["matchBase"]) || isTrue(opts["basename"]),
	}
	if ignore != nil {
		matchOpts.Ignore = make([]string, len(ignore))
		for i, ig := range ignore {
			s, ok := ig.(string)
			if !ok {
				return nil, errors.New("glob.slice is not a function")
			}
			matchOpts.Ignore[i] = glob.ToAbsolute(s, gs.cwd, root, wd)
		}
	}
	if gs.matcher, err = glob.NewMatcher(abs, matchOpts); err != nil {
		return nil, err
	}
	return gs, nil
}

// rootOption reads the root option as to-absolute-glob uses it: falsy means unset and a truthy non-string fails
// in its unescape(path) helper.
func rootOption(v any) (string, error) {
	if !truthy(v) {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", errors.New("path.replace is not a function")
	}
	return s, nil
}

// walkAction is one queued walker task.
type walkAction struct {
	kind string // "walk", "exists" or "resolve"
	path string
}

// run walks the file system and returns every matching, unique entry in the order glob-stream pushes them. The
// walk stops at the first walker error; a positive non-glob that matched nothing is the singular glob error at
// the end of the walk.
func (gs *globStream) run() ([]globEntry, *walkError) {
	var entries []globEntry
	var clock stepClock
	found := make([]bool, len(gs.positiveGlobs))
	var queue []walkAction
	for i, g := range gs.positiveGlobs {
		found[i] = glob.IsGlob(g)
		if found[i] {
			queue = append(queue, walkAction{"walk", glob.Parent(g)})
		} else {
			queue = append(queue, walkAction{"exists", g})
		}
	}
	seen := map[any]bool{}

	onPath := func(p string, isDir bool) {
		idx := gs.matcher.Index(p)
		if idx == -1 && isDir {
			idx = gs.matcher.Index(p + "/")
		}
		if idx == -1 {
			return
		}
		pos := gs.positiveIndex[idx]
		found[pos] = true
		base := gs.base
		if base == "" {
			base = glob.Parent(gs.positiveGlobs[pos])
		}
		e := globEntry{cwd: gs.cwd, base: base, path: glob.NormalizePath(p, true), at: clock.now}
		key, comparable := gs.uniqueKey(e)
		if comparable {
			if seen[key] {
				return
			}
			seen[key] = true
		}
		entries = append(entries, e)
	}

	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		switch a.kind {
		case "walk":
			clock.step()
			dirents, err := os.ReadDir(a.path)
			if err != nil {
				if hasErrno(err, syscall.EACCES) || hasErrno(err, syscall.ENOENT) {
					continue
				}
				return entries, &walkError{nodeErr("scandir", a.path, err), clock.now}
			}
			for _, d := range dirents {
				next := nodepath.Join(a.path, d.Name())
				onPath(next, d.IsDir())
				if d.IsDir() {
					queue = append(queue, walkAction{"walk", next})
				} else if d.Type()&os.ModeSymlink != 0 {
					queue = append(queue, walkAction{"resolve", next})
				}
			}
		case "exists":
			clock.step()
			st, err := statFile(a.path)
			if err != nil {
				continue
			}
			onPath(a.path, st.IsDirectory())
		case "resolve":
			real, err := realpath(a.path, &clock)
			if err != nil {
				return entries, &walkError{err, clock.now}
			}
			clock.step()
			st, err := lstatFile(real)
			if err != nil {
				return entries, &walkError{err, clock.now}
			}
			if st.IsDirectory() && !strings.HasPrefix(a.path, real+"/") {
				queue = append(queue, walkAction{"walk", a.path})
			}
		}
	}

	if !gs.allowEmpty {
		for i, ok := range found {
			if !ok {
				return entries, &walkError{errors.New(globErrMessage1 + gs.positiveGlobs[i] + globErrMessage2), clock.now}
			}
		}
	}
	return entries, nil
}

// uniqueKey is the value uniqueBy compares; comparable is false for values JavaScript compares by identity.
func (gs *globStream) uniqueKey(e globEntry) (key any, comparable bool) {
	switch by := gs.uniqueBy.(type) {
	case string:
		switch by {
		case "path":
			return e.path, true
		case "cwd":
			return e.cwd, true
		case "base":
			return e.base, true
		}
		return undefinedKey{}, true
	}
	f := vinyl.New(vinyl.Options{Cwd: e.cwd, Base: e.base, Path: e.path})
	out, _ := callWithFile(gs.uniqueBy, f)
	if out == nil {
		return undefinedKey{}, true
	}
	if n, ok := toNumber(out); ok {
		return n, true
	}
	switch out.(type) {
	case string, bool:
		return out, true
	}
	return nil, false
}

// isTrue reports whether v is the boolean true: picomatch tests nobrace, noglobstar, noextglob, matchBase and
// basename with === true, so a truthy non-boolean value leaves the feature on.
func isTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// noExtglob follows picomatch's parse: a boolean noext overrides noextglob, which counts only when it is true.
func noExtglob(opts Options) bool {
	if b, ok := opts["noext"].(bool); ok {
		return b
	}
	return isTrue(opts["noextglob"])
}
