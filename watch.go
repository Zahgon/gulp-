package gulp

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gulpjs/gulp/internal/eventloop"
	"github.com/gulpjs/gulp/internal/glob"
	"github.com/gulpjs/gulp/internal/jsnum"
	"github.com/gulpjs/gulp/internal/nodepath"
	"github.com/gulpjs/gulp/vfs"
)

// pollInterval is how often a Watcher scans the watched paths when the usePolling option is off.
const pollInterval = 50 * time.Millisecond

// Watcher watches files (the chokidar 3.6 FSWatcher gulp.watch returns), implemented by scanning the watched paths.
type Watcher struct {
	opts        watchConfig  // fixed when the watcher is created
	userIgnored []ignoreRule // the ignored option
	cwd         string       // absolute cwd option, "" when it is unset

	scanMu sync.Mutex // serializes scans: the poller's, Add's and Unwatch's (ignored functions are never called concurrently)

	mu           sync.Mutex
	closed       bool
	stop         chan struct{}
	roots        []watchRoot
	ignoredPaths []ignoredPath         // negated globs and unwatched paths
	deferred     []string              // negated globs of an Add that takes effect at the next Add of a path or Unwatch
	kept         map[string]bool       // files Unwatch left watched although no scan reaches them any more
	state        map[string]entryState // the last scan
	gen          uint64                // changes whenever Add or Unwatch changes roots, ignoredPaths or state
	unreported   error                 // first file system error no error listener received

	// loop goroutine only
	listeners      map[string][]watchListener
	errorFns       []func(error)
	initialErrs    []error // file system errors of the first scan, delivered once listeners can be attached
	refed          bool
	readyEmitted   bool
	pendingWrites  map[string]*pendingWrite
	pendingUnlinks []string
}

// watchListener receives one event with its name, reported path and file information (nil when there is none).
type watchListener func(event, path string, info os.FileInfo)

// watchRoot is one positive path or glob being watched.
type watchRoot struct {
	path     string          // directory or file scanned (internal form: absolute with cwd, else as given)
	matcher  *glob.Matcher   // nil for a plain path
	dirParts []*glob.Matcher // folder-level segments of the glob below path (nil for a globstar), for symbolic links
	maxDepth int             // directory levels below path the glob can reach, -1 for unlimited
	isGlob   bool
}

// ignoredPath is one negated glob or unwatched path.
type ignoredPath struct {
	pattern string
	m       *glob.Matcher
}

// ignoreRule is one value of the ignored option: a glob, a function or a regular expression.
type ignoreRule struct {
	m  *glob.Matcher
	fn func(path string, info os.FileInfo) bool
	re *regexp.Regexp
}

// match reports whether the rule ignores p. A function is asked without and with the file information, as the
// original asks it.
func (r ignoreRule) match(p string, info os.FileInfo) bool {
	switch {
	case r.m != nil:
		return r.m.Index(p) >= 0
	case r.re != nil:
		return r.re.MatchString(p)
	case r.fn != nil:
		return r.fn(p, nil) || (info != nil && r.fn(p, info))
	}
	return false
}

// entryState is what the scanner compares between scans.
type entryState struct {
	dir   bool
	size  int64
	mtime time.Time
	ino   uint64
	mode  os.FileMode
	info  os.FileInfo
}

// pendingWrite is a file event held by awaitWriteFinish until the file's size settles.
type pendingWrite struct {
	event      string
	lastChange time.Time
	timer      *eventloop.Timer
}

// Watch watches globs (a string or []string, negated globs excluded) and runs task, wrapped in Parallel so the
// command line logs it, after changes settle (gulp.watch over glob-watcher 6 and chokidar 3.6).
//
// Options are glob-watcher's and chokidar's: delay (ms, default 200), queue (default true), events (default add,
// change, unlink), ignoreInitial (default true), ignored (a glob, a func(path string) bool, a
// func(path string, info os.FileInfo) bool, a *regexp.Regexp, or a list of them), cwd, persistent (default true:
// the watcher keeps a command line run alive until it is closed), followSymlinks (default true), disableGlobbing
// (default false), depth (folder levels read below each watched folder, default unlimited), awaitWriteFinish
// (false, true or a map with stabilityThreshold and pollInterval in ms, defaults 2000 and 100),
// ignorePermissionErrors (default false), usePolling (default false), interval and binaryInterval (polling periods
// in ms, defaults 100 and 300), useFsEvents (no effect: there is no fsevents), alwaysStat (default false) and atomic
// (default true unless usePolling: editor swap files are ignored and an unlink followed by an add within 100 ms, or
// the given number of ms, is reported as one change). The CHOKIDAR_USEPOLLING and CHOKIDAR_INTERVAL environment
// variables override usePolling and interval. Functions in ignored are called one at a time from the watcher's
// scanning goroutine and must not call the watcher's methods.
//
// task may be nil, a *Task or a TaskFunc; a name or a list of names returns the original's error "watching
// <globs>: watch task has to be a function (optionally generated by using gulp.parallel or gulp.series)", and any
// other value is ignored, as the original ignores a task argument that is not a function. globs of another type
// return the error "Non-string provided as watch path: <globs>".
func (g *Gulp) Watch(globs any, opts vfs.Options, task any) (*Watcher, error) {
	switch task.(type) {
	case string, []string, []any:
		return nil, fmt.Errorf("watching %s: watch task has to be a function (optionally generated by using gulp.parallel or gulp.series)", globText(globs))
	}
	var fn *Task
	switch v := task.(type) {
	case *Task:
		if v != nil && v.Fn != nil {
			fn = v
		}
	case TaskFunc:
		if v != nil {
			fn = &Task{Fn: v}
		}
	case func(Done):
		if v != nil {
			fn = &Task{Fn: v}
		}
	}
	var run *Task
	if fn != nil {
		p, err := g.Parallel(fn)
		if err != nil {
			return nil, err
		}
		run = p
	}
	o, err := watchOptions(opts)
	if err != nil {
		return nil, err
	}
	list, err := globList(globs)
	if err != nil {
		return nil, err
	}
	w, err := newWatcher(list, o)
	if err != nil {
		return nil, err
	}
	eventloop.OnHalt(w.stopPolling)
	if !o.persistent {
		eventloop.OnRunEnd(w.stopPolling)
	}
	eventloop.Dispatch(func() {
		if o.persistent {
			w.ref()
		}
		w.registerTask(o, run)
		w.initialEvents(o.ignoreInitial)
	})
	if errs := w.initialErrs; len(errs) > 0 {
		eventloop.Post(func() {
			for _, err := range errs {
				w.deliverError(err)
			}
		})
	}
	go w.poll()
	return w, nil
}

// globText is String(glob) for the error message.
func globText(globs any) string {
	switch v := globs.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, ",")
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			if x != nil {
				parts[i], _ = jsKey(x)
			}
		}
		return strings.Join(parts, ",")
	}
	s, _ := jsKey(globs)
	return s
}

// globList returns globs as a list of strings, or chokidar's error for a value that holds anything else.
func globList(globs any) ([]string, error) {
	switch v := globs.(type) {
	case string:
		return []string{v}, nil
	case []string:
		return append([]string(nil), v...), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("Non-string provided as watch path: %s", globText(globs))
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("Non-string provided as watch path: %s", globText(globs))
}

// watchConfig follows glob-watcher's and chokidar's defaults merged with the given options.
type watchConfig struct {
	delay                  time.Duration
	events                 []string
	queue                  bool
	ignoreInitial          bool
	ignored                any
	cwd                    string
	persistent             bool
	followSymlinks         bool
	disableGlobbing        bool
	depth                  float64
	hasDepth               bool
	awaitWriteFinish       bool
	stabilityThreshold     time.Duration
	awfPollInterval        time.Duration
	ignorePermissionErrors bool
	usePolling             bool
	interval               time.Duration
	binaryInterval         time.Duration
	alwaysStat             bool
	atomic                 bool
	atomicDelay            time.Duration
}

// watchOptions reads glob-watcher's and chokidar's options with their defaults. glob-watcher's options follow
// Object.assign (a key present with a nil value is undefined and overrides the default); chokidar replaces only
// undefined options with its defaults. The error reports a cwd option that is not a string.
func watchOptions(opts vfs.Options) (watchConfig, error) {
	c := watchConfig{delay: 200 * time.Millisecond, events: []string{"add", "change", "unlink"}, queue: true,
		ignoreInitial: true, persistent: true, followSymlinks: true, interval: 100 * time.Millisecond,
		binaryInterval: 300 * time.Millisecond, atomicDelay: 100 * time.Millisecond}
	if v, ok := opts["delay"]; ok {
		c.delay = timeoutDuration(v)
	}
	if v, ok := opts["events"]; ok {
		switch e := v.(type) {
		case []string:
			c.events = append([]string(nil), e...)
		case string:
			c.events = []string{e}
		case []any:
			c.events = nil
			for _, x := range e {
				if s, isString := x.(string); isString {
					c.events = append(c.events, s)
				}
			}
		default:
			c.events = nil
		}
	}
	if v, ok := opts["queue"]; ok {
		c.queue = truthy(v)
	}
	if v, ok := opts["ignoreInitial"]; ok {
		c.ignoreInitial = truthy(v)
	}
	c.ignored = opts["ignored"]
	if v, present := opts["cwd"]; present && v != nil {
		s, ok := v.(string)
		if !ok {
			// chokidar resolves paths with path.relative(cwd, ...), which throws for a cwd that is not a string.
			return c, fmt.Errorf("The \"cwd\" option must be of type string. Received %T", v)
		}
		c.cwd = s
	}
	defined := func(key string) (any, bool) {
		v, ok := opts[key]
		return v, ok && v != nil
	}
	if v, ok := defined("persistent"); ok {
		c.persistent = truthy(v)
	}
	if v, ok := defined("followSymlinks"); ok {
		c.followSymlinks = truthy(v)
	}
	if v, ok := defined("disableGlobbing"); ok {
		c.disableGlobbing = truthy(v)
	}
	if v, ok := defined("ignorePermissionErrors"); ok {
		c.ignorePermissionErrors = truthy(v)
	}
	if v, ok := defined("alwaysStat"); ok {
		c.alwaysStat = truthy(v)
	}
	if v, ok := defined("depth"); ok {
		c.depth, c.hasDepth = toNumber(v), true
	}
	if v, ok := defined("interval"); ok {
		c.interval = pollDuration(v)
	}
	if v, ok := defined("binaryInterval"); ok {
		c.binaryInterval = pollDuration(v)
	}
	if v, ok := defined("usePolling"); ok {
		c.usePolling = truthy(v)
	}
	if env, ok := os.LookupEnv("CHOKIDAR_USEPOLLING"); ok {
		lower := strings.ToLower(env)
		c.usePolling = lower != "false" && lower != "0" && lower != ""
	}
	if env := os.Getenv("CHOKIDAR_INTERVAL"); env != "" {
		c.interval = pollDuration(jsnum.ParseInt(env))
	}
	c.atomic = !c.usePolling
	if v, ok := defined("atomic"); ok {
		c.atomic = truthy(v)
		if n, isNumber := v.(float64); isNumber {
			c.atomicDelay = timeoutDuration(n)
		}
	}
	if v, ok := defined("awaitWriteFinish"); ok && truthy(v) {
		c.awaitWriteFinish = true
		c.stabilityThreshold, c.awfPollInterval = 2000*time.Millisecond, 100*time.Millisecond
		if m, isMap := v.(map[string]any); isMap {
			if t := toNumber(m["stabilityThreshold"]); truthy(t) {
				c.stabilityThreshold = msDuration(t)
			}
			if t := toNumber(m["pollInterval"]); truthy(t) {
				c.awfPollInterval = timeoutDuration(t)
			}
		}
	}
	return c, nil
}

// toNumber is JavaScript's Number(value) for option values; nil (undefined) and other types are NaN.
func toNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		return jsnum.Number(x)
	}
	return math.NaN()
}

// msDuration converts milliseconds; NaN and values below zero are 0.
func msDuration(ms float64) time.Duration {
	if !(ms > 0) {
		return 0
	}
	if ms >= float64(math.MaxInt64/int64(time.Millisecond)) {
		return math.MaxInt64
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// pollDuration is a polling period: fs.watchFile takes values below 1 ms, and values that are not numbers, as its
// 5007 ms default.
func pollDuration(v any) time.Duration {
	ms := toNumber(v)
	if !(ms >= 1) {
		return 5007 * time.Millisecond
	}
	return msDuration(ms)
}

// timeoutDuration is setTimeout's delay coercion: non-numbers and values outside [1, 2^31-1] become 1 ms.
func timeoutDuration(v any) time.Duration {
	var ms float64
	switch x := v.(type) {
	case int:
		ms = float64(x)
	case int64:
		ms = float64(x)
	case float64:
		ms = x
	case time.Duration:
		ms = float64(x) / float64(time.Millisecond)
	default:
		ms = math.NaN()
	}
	if !(ms >= 1 && ms <= 1<<31-1) {
		ms = 1
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// truthy is JavaScript truthiness for option values.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int:
		return x != 0
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

// swapFile follows chokidar's DOT_RE: editor swap and temporary files ignored in atomic mode.
var swapFile = regexp.MustCompile(`\..*\.(sw[px])$|~$|\.subl.*\.tmp`)

func newWatcher(globs []string, c watchConfig) (*Watcher, error) {
	w := &Watcher{
		opts:          c,
		stop:          make(chan struct{}),
		state:         map[string]entryState{},
		kept:          map[string]bool{},
		listeners:     map[string][]watchListener{},
		pendingWrites: map[string]*pendingWrite{},
	}
	if c.cwd != "" {
		abs, err := nodepath.Resolve(c.cwd)
		if err != nil {
			return nil, err
		}
		w.cwd = abs
	}
	rules, err := w.ignoreRules(c.ignored)
	if err != nil {
		return nil, err
	}
	w.userIgnored = rules
	if _, err := w.addPaths(globs, false); err != nil {
		return nil, err
	}
	w.state, w.initialErrs = w.scan(w.config())
	return w, nil
}

// ignoreRules reads the ignored option. A string is a glob resolved against cwd (a plain path also ignores
// everything below it); values of other types match nothing, as in the original.
func (w *Watcher) ignoreRules(v any) ([]ignoreRule, error) {
	var out []ignoreRule
	addGlob := func(p string) error {
		if !nodepath.IsAbs(p) {
			p = nodepath.Join(w.cwd, p)
		}
		patterns := []string{p}
		if !glob.IsGlob(p) {
			patterns = append(patterns, p+"/**")
		}
		for _, g := range patterns {
			m, err := glob.NewMatcher([]string{g}, glob.Options{Dot: true})
			if err != nil {
				return err
			}
			out = append(out, ignoreRule{m: m})
		}
		return nil
	}
	var add func(v any, top bool) error
	add = func(v any, top bool) error {
		switch x := v.(type) {
		case string:
			return addGlob(x)
		case []string:
			for _, s := range x {
				if err := addGlob(s); err != nil {
					return err
				}
			}
		case []any:
			if !top {
				return nil
			}
			for _, e := range x {
				if err := add(e, false); err != nil {
					return err
				}
			}
		case func(string) bool:
			if x != nil {
				out = append(out, ignoreRule{fn: func(p string, _ os.FileInfo) bool { return x(p) }})
			}
		case func(string, os.FileInfo) bool:
			if x != nil {
				out = append(out, ignoreRule{fn: x})
			}
		case *regexp.Regexp:
			if x != nil {
				out = append(out, ignoreRule{re: x})
			}
		}
		return nil
	}
	return out, add(v, true)
}

// addPaths adds paths and globs as chokidar's add does: negated globs become ignored paths, other paths stop being
// ignored and are watched. It returns the new roots. The caller holds mu or owns the watcher.
//
// chokidar caches its ignore test and drops the cache only for a path that is not negated, or in unwatch. A running
// watcher therefore applies the negated globs of an add only when that add, a later one, or an unwatch also drops
// the cache; until then they are kept in deferred.
func (w *Watcher) addPaths(globs []string, running bool) ([]watchRoot, error) {
	var added []watchRoot
	resets := !running
	for _, p := range globs {
		if !strings.HasPrefix(p, "!") {
			resets = true
		}
	}
	for _, p := range globs {
		neg := strings.HasPrefix(p, "!")
		if neg {
			p = p[1:]
		}
		p = w.internalPath(p)
		if neg {
			w.deferred = append(w.deferred, p)
			continue
		}
		w.unignorePath(p)
		w.unignorePath(p + "/**")
		root, err := w.newRoot(p)
		if err != nil {
			return nil, err
		}
		w.roots = append(w.roots, root)
		added = append(added, root)
	}
	if resets {
		if err := w.applyDeferred(); err != nil {
			return nil, err
		}
	}
	return added, nil
}

// applyDeferred makes the deferred negated globs ignored paths (a plain path also ignores everything below it).
func (w *Watcher) applyDeferred() error {
	for _, p := range w.deferred {
		if err := w.ignorePath(p); err != nil {
			return err
		}
		if !glob.IsGlob(p) {
			if err := w.ignorePath(p + "/**"); err != nil {
				return err
			}
		}
	}
	w.deferred = nil
	return nil
}

// newRoot prepares one watched path or glob.
func (w *Watcher) newRoot(p string) (watchRoot, error) {
	root := watchRoot{path: p, maxDepth: -1}
	if w.opts.disableGlobbing || !glob.IsGlob(p) {
		return root, nil
	}
	m, err := glob.NewMatcher([]string{p}, glob.Options{Dot: true})
	if err != nil {
		return root, err
	}
	root.isGlob = true
	root.matcher = m
	root.path = glob.Parent(p)
	rest := strings.TrimPrefix(strings.TrimPrefix(p, root.path), "/")
	if !strings.Contains(rest, "**") {
		root.maxDepth = strings.Count(rest, "/")
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	for _, part := range parts {
		if part == "**" {
			root.dirParts = append(root.dirParts, nil)
			break
		}
		pm, err := glob.NewMatcher([]string{part}, glob.Options{Dot: true})
		if err != nil {
			return root, err
		}
		root.dirParts = append(root.dirParts, pm)
	}
	return root, nil
}

func (w *Watcher) ignorePath(p string) error {
	for _, ip := range w.ignoredPaths {
		if ip.pattern == p {
			return nil
		}
	}
	m, err := glob.NewMatcher([]string{p}, glob.Options{Dot: true})
	if err != nil {
		return err
	}
	w.ignoredPaths = append(w.ignoredPaths, ignoredPath{pattern: p, m: m})
	return nil
}

func (w *Watcher) unignorePath(p string) {
	out := w.ignoredPaths[:0]
	for _, ip := range w.ignoredPaths {
		if ip.pattern != p {
			out = append(out, ip)
		}
	}
	w.ignoredPaths = out
}

// internalPath is the path chokidar works with: resolved against cwd (absolute) when the option is set, otherwise
// the normalised path as given.
func (w *Watcher) internalPath(p string) string {
	if w.cwd != "" {
		return nodepath.ResolveIn(w.cwd, p)
	}
	return nodepath.Normalize(p)
}

// reportPath is the path an event reports: relative to cwd when the option is set.
func (w *Watcher) reportPath(p string) string {
	if w.cwd != "" {
		return nodepath.RelativeIn(w.cwd, w.cwd, p)
	}
	return p
}

// fullPath turns a reported path back into one the file system resolves.
func (w *Watcher) fullPath(rel string) string {
	if w.cwd != "" && !nodepath.IsAbs(rel) {
		return nodepath.Join(w.cwd, rel)
	}
	return rel
}

// scanConfig is the part of the watcher a scan reads, copied under mu.
type scanConfig struct {
	roots   []watchRoot
	ignored []ignoredPath
	kept    map[string]bool
	gen     uint64
}

func (w *Watcher) config() scanConfig {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.configLocked()
}

func (w *Watcher) configLocked() scanConfig {
	kept := make(map[string]bool, len(w.kept))
	for k := range w.kept {
		kept[k] = true
	}
	return scanConfig{roots: append([]watchRoot(nil), w.roots...),
		ignored: append([]ignoredPath(nil), w.ignoredPaths...), kept: kept, gen: w.gen}
}

func (w *Watcher) isIgnored(c scanConfig, p string, info os.FileInfo) bool {
	if w.opts.atomic && swapFile.MatchString(p) {
		return true
	}
	for _, ip := range c.ignored {
		if ip.pattern == p || ip.m.Index(p) >= 0 {
			return true
		}
	}
	for _, r := range w.userIgnored {
		if r.match(p, info) {
			return true
		}
	}
	return false
}

// readable is chokidar's read permission test for files met in a folder: the first octal digit of the permission
// bits, written without leading zeros, must have the read bit (so 0o044 counts as readable and 0o200 does not).
// With ignorePermissionErrors every file passes.
func (w *Watcher) readable(info os.FileInfo) bool {
	if w.opts.ignorePermissionErrors {
		return true
	}
	digits := strconv.FormatUint(uint64(info.Mode().Perm()), 8)
	return (digits[0]-'0')&4 != 0
}

// readsLevel reports whether a folder at the given level below a watched path is read (the depth option).
func (w *Watcher) readsLevel(level int) bool {
	return !w.opts.hasDepth || float64(level) <= w.opts.depth
}

// stat reads an entry, following symbolic links unless followSymlinks is off.
func (w *Watcher) stat(p string) (os.FileInfo, error) {
	if w.opts.followSymlinks {
		return os.Stat(p)
	}
	return os.Lstat(p)
}

// scan collects the state of every watched entry. Entries that do not exist (or vanished while the scan ran) are
// simply absent, as chokidar treats ENOENT and ENOTDIR; every other file system error is returned for the
// watcher's error listeners, except permission errors when ignorePermissionErrors is set.
func (w *Watcher) scan(c scanConfig) (map[string]entryState, []error) {
	out := map[string]entryState{}
	var errs []error
	for _, r := range c.roots {
		info, err := w.stat(r.path)
		if err != nil {
			errs = w.appendScanError(errs, err)
			continue
		}
		if !r.isGlob {
			if w.isIgnored(c, r.path, info) {
				continue
			}
			out[r.path] = stateOf(info)
			if info.IsDir() && w.readsLevel(0) {
				errs = w.walk(c, r, r.path, 0, out, errs, map[string]bool{})
			}
			continue
		}
		if info.IsDir() {
			if r.matcher.Index(r.path) >= 0 && !w.isIgnored(c, r.path, info) {
				out[r.path] = stateOf(info)
			}
			if w.readsLevel(0) {
				errs = w.walk(c, r, r.path, 0, out, errs, map[string]bool{})
			}
		}
	}
	for p := range c.kept {
		if _, ok := out[p]; ok {
			continue
		}
		info, err := w.stat(p)
		if err != nil {
			errs = w.appendScanError(errs, err)
			continue
		}
		if !info.IsDir() {
			out[p] = stateOf(info)
		}
	}
	return out, errs
}

// appendScanError adds err to errs unless it only says the entry is missing, which a scan expects, or it is a
// permission error and ignorePermissionErrors is set.
func (w *Watcher) appendScanError(errs []error, err error) []error {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return errs
	}
	if w.opts.ignorePermissionErrors && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)) {
		return errs
	}
	return append(errs, err)
}

// walk records the entries below dir that the root watches and returns errs with the file system errors met.
// A folder reached again through a symbolic link while it is being read is not read twice, so a link to an
// enclosing folder ends.
func (w *Watcher) walk(c scanConfig, r watchRoot, dir string, depth int, out map[string]entryState, errs []error, open map[string]bool) []error {
	real, err := realPath(dir)
	if err != nil {
		return w.appendScanError(errs, err)
	}
	if open[real] {
		return errs
	}
	open[real] = true
	defer delete(open, real)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return w.appendScanError(errs, err)
	}
	for _, e := range entries {
		p := nodepath.Join(dir, e.Name())
		info, err := os.Lstat(p)
		if err != nil {
			errs = w.appendScanError(errs, err)
			continue
		}
		isLink := info.Mode()&os.ModeSymlink != 0
		if isLink && w.opts.followSymlinks {
			target, err := os.Stat(p)
			if err != nil {
				errs = w.appendScanError(errs, err)
				continue
			}
			info = target
		}
		if w.isIgnored(c, p, info) {
			continue
		}
		if !info.IsDir() && !isLink && !w.readable(info) {
			continue
		}
		switch {
		case !r.isGlob:
			out[p] = stateOf(info)
		case isLink && !info.IsDir():
			// chokidar tests symbolic links with the folder segments of the glob only.
			if r.matchesDirParts(p) {
				out[p] = stateOf(info)
			}
		case r.matcher.Index(p) >= 0:
			out[p] = stateOf(info)
		}
		if info.IsDir() && (r.maxDepth < 0 || depth < r.maxDepth) && w.readsLevel(depth+1) {
			errs = w.walk(c, r, p, depth+1, out, errs, open)
		}
	}
	return errs
}

// matchesDirParts is chokidar's folder filter: each path segment below the glob's parent matches the glob's segment
// at the same place, until a globstar.
func (r watchRoot) matchesDirParts(p string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(p, r.path), "/")
	entry := strings.Split(rel, "/")
	for i, part := range r.dirParts {
		if part == nil {
			return true
		}
		if i >= len(entry) || entry[i] == "" {
			continue
		}
		if part.Index(entry[i]) < 0 {
			return false
		}
	}
	return true
}

// realPath resolves every symbolic link in p.
func realPath(p string) (string, error) {
	abs, err := nodepath.Resolve(p)
	if err != nil {
		return "", err
	}
	resolved := "/"
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for hops := 0; len(parts) > 0; {
		name := parts[0]
		parts = parts[1:]
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			resolved = nodepath.Dirname(resolved)
			continue
		}
		next := nodepath.Join(resolved, name)
		info, err := os.Lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		if hops++; hops > 40 {
			return "", syscall.ELOOP
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if nodepath.IsAbs(target) {
			resolved = "/"
		}
		parts = append(strings.Split(strings.TrimPrefix(target, "/"), "/"), parts...)
	}
	return resolved, nil
}

// fsEvent is one change the scanner found.
type fsEvent struct {
	name string
	path string
	info os.FileInfo
}

// diff lists the events between two scans: removals in path order, then additions and changes in modification
// order (the order the file system reported them in), ties in path order.
func diff(old, cur map[string]entryState) []fsEvent {
	var removed, touched []string
	for p, s := range old {
		n, ok := cur[p]
		switch {
		case !ok || n.dir != s.dir:
			removed = append(removed, p)
			if ok {
				touched = append(touched, p)
			}
		case !n.dir && (n.size != s.size || !n.mtime.Equal(s.mtime) || n.ino != s.ino || n.mode != s.mode):
			touched = append(touched, p)
		}
	}
	for p := range cur {
		if _, ok := old[p]; !ok {
			touched = append(touched, p)
		}
	}
	sort.Strings(removed)
	sort.Slice(touched, func(a, b int) bool {
		ta, tb := cur[touched[a]].mtime, cur[touched[b]].mtime
		if !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return touched[a] < touched[b]
	})
	var out []fsEvent
	for _, p := range removed {
		name := "unlink"
		if old[p].dir {
			name = "unlinkDir"
		}
		out = append(out, fsEvent{name: name, path: p})
	}
	for _, p := range touched {
		s, existed := old[p]
		name := "change"
		if !existed || s.dir != cur[p].dir {
			name = "add"
			if cur[p].dir {
				name = "addDir"
			}
		}
		out = append(out, fsEvent{name: name, path: p, info: cur[p].info})
	}
	return out
}

// throttleChanges follows chokidar's 50 ms change throttle: a change for a path whose change the previous scan (one
// pollInterval earlier) delivered is dropped. One write can show up in two scans, since the kernel updates the
// modification time before the size. It returns the events to deliver and the paths whose change they deliver.
func throttleChanges(events []fsEvent, throttled map[string]bool) ([]fsEvent, map[string]bool) {
	out := events[:0]
	next := map[string]bool{}
	for _, e := range events {
		if e.name == "change" {
			if throttled[e.path] {
				continue
			}
			next[e.path] = true
		}
		out = append(out, e)
	}
	return out, next
}

// binaryExt reports whether chokidar polls p with binaryInterval (is-binary-path).
func binaryExt(p string) bool {
	return binaryExtensions[strings.ToLower(strings.TrimPrefix(nodepath.Extname(p), "."))]
}

// poll scans until the watcher is closed and delivers the events on the loop. With usePolling, text files are
// compared every interval and binary files every binaryInterval; otherwise every entry is compared every
// pollInterval.
func (w *Watcher) poll() {
	period := pollInterval
	if w.opts.usePolling {
		period = min(w.opts.interval, w.opts.binaryInterval)
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	lastText, lastBinary := time.Now(), time.Now()
	var throttled map[string]bool
	fresh := map[string]entryState{}
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
		}
		textDue, binaryDue := true, true
		if w.opts.usePolling {
			now := time.Now()
			textDue = now.Sub(lastText) >= w.opts.interval-period/2
			binaryDue = now.Sub(lastBinary) >= w.opts.binaryInterval-period/2
			if textDue {
				lastText = now
			}
			if binaryDue {
				lastBinary = now
			}
		}
		w.scanMu.Lock()
		c := w.config()
		cur, errs := w.scan(c)
		w.mu.Lock()
		if w.gen != c.gen || w.closed {
			w.mu.Unlock()
			w.scanMu.Unlock()
			continue
		}
		prev := w.state
		if !textDue || !binaryDue {
			carryOver(prev, cur, func(p string) bool {
				if binaryExt(p) {
					return binaryDue
				}
				return textDue
			})
		}
		settleNewFiles(prev, cur, fresh)
		found := diff(prev, cur)
		w.state = cur
		keptGone := map[string]bool{}
		for _, e := range found {
			if e.name == "unlink" && w.kept[e.path] {
				delete(w.kept, e.path)
				keptGone[e.path] = true
			}
		}
		w.mu.Unlock()
		events := found[:0]
		for _, e := range found {
			// a removed file left watched by Unwatch is reported only when nothing ignores it
			if !keptGone[e.path] || !w.isIgnored(c, e.path, nil) {
				events = append(events, e)
			}
		}
		w.scanMu.Unlock()
		events, throttled = throttleChanges(events, throttled)
		if len(events) == 0 && len(errs) == 0 {
			continue
		}
		eventloop.Post(func() {
			for _, e := range events {
				w.deliver(e.name, e.path, e.info)
			}
			for _, err := range errs {
				w.deliverError(err)
			}
		})
	}
}

// settleNewFiles holds back a file that appeared since the previous scan until a second scan finds it unchanged,
// so a file seen between its creation and the write that follows is reported once, as one add with its contents,
// the way chokidar reports a file written in one call. fresh carries the held files between scans.
func settleNewFiles(prev, cur, fresh map[string]entryState) {
	for p := range fresh {
		if _, ok := cur[p]; !ok {
			delete(fresh, p)
		}
	}
	for p, s := range cur {
		if _, known := prev[p]; known || s.dir {
			continue
		}
		if held, ok := fresh[p]; ok && sameState(held, s) {
			delete(fresh, p)
			continue
		}
		fresh[p] = s
		delete(cur, p)
	}
}

// sameState reports whether two scans saw the same file contents and identity.
func sameState(a, b entryState) bool {
	return a.size == b.size && a.mtime.Equal(b.mtime) && a.ino == b.ino && a.mode == b.mode
}

// carryOver keeps the previous state of the entries whose polling period has not come yet.
func carryOver(prev, cur map[string]entryState, due func(string) bool) {
	for p, s := range prev {
		if !due(p) {
			cur[p] = s
		}
	}
	for p := range cur {
		if _, ok := prev[p]; !ok && !due(p) {
			delete(cur, p)
		}
	}
}

func (w *Watcher) snapshot() map[string]entryState {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]entryState, len(w.state))
	for k, v := range w.state {
		out[k] = v
	}
	return out
}

// initialEvents emits add and addDir for the entries found at start when ignoreInitial is off, then ready.
func (w *Watcher) initialEvents(ignoreInitial bool) {
	if ignoreInitial {
		eventloop.Post(w.emitReady)
		return
	}
	snap := w.snapshot()
	eventloop.Post(func() {
		w.deliverAdds(snap)
		w.emitReady()
	})
}

// deliverAdds emits add or addDir for each entry, in path order.
func (w *Watcher) deliverAdds(entries map[string]entryState) {
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		name := "add"
		if entries[p].dir {
			name = "addDir"
		}
		w.deliver(name, p, entries[p].info)
	}
}

func (w *Watcher) emitReady() {
	if w.isClosed() {
		return
	}
	w.readyEmitted = true
	for _, fn := range append([]watchListener{}, w.listeners["ready"]...) {
		fn("ready", "", nil)
	}
}

// deliver applies chokidar's awaitWriteFinish and atomic handling to one event of an internal path, then emits it.
func (w *Watcher) deliver(name, p string, info os.FileInfo) {
	if w.isClosed() {
		return
	}
	rel := w.reportPath(p)
	awf := w.opts.awaitWriteFinish
	if awf && (name == "unlink" || name == "unlinkDir") {
		if pw := w.pendingWrites[rel]; pw != nil {
			delete(w.pendingWrites, rel)
			pw.timer.Stop()
			if pw.event == "add" {
				return
			}
		}
	}
	if pw := w.pendingWrites[rel]; awf && pw != nil {
		pw.lastChange = time.Now()
		return
	}
	if w.opts.atomic {
		if name == "unlink" {
			w.holdUnlink(rel)
			return
		}
		if name == "add" && w.dropPendingUnlink(rel) {
			name = "change"
		}
	}
	if awf && (name == "add" || name == "change") && w.readyEmitted {
		w.awaitWriteFinish(name, rel)
		return
	}
	w.emit(name, rel, info)
}

// holdUnlink keeps an unlink for the atomic delay; every held unlink is emitted when the delay of any ends.
func (w *Watcher) holdUnlink(rel string) {
	w.dropPendingUnlink(rel)
	w.pendingUnlinks = append(w.pendingUnlinks, rel)
	eventloop.AfterFunc(w.opts.atomicDelay, func() {
		held := w.pendingUnlinks
		w.pendingUnlinks = nil
		for _, p := range held {
			w.emit("unlink", p, nil)
		}
	})
}

// dropPendingUnlink removes a held unlink of rel and reports whether there was one.
func (w *Watcher) dropPendingUnlink(rel string) bool {
	for i, p := range w.pendingUnlinks {
		if p == rel {
			w.pendingUnlinks = append(w.pendingUnlinks[:i:i], w.pendingUnlinks[i+1:]...)
			return true
		}
	}
	return false
}

// awaitWriteFinish holds an add or change until the file's size has not changed for stabilityThreshold, checking
// every pollInterval; further events for the path meanwhile only restart the wait.
func (w *Watcher) awaitWriteFinish(name, rel string) {
	pw := &pendingWrite{event: name, lastChange: time.Now()}
	w.pendingWrites[rel] = pw
	full := w.fullPath(rel)
	var check func(prev os.FileInfo)
	check = func(prev os.FileInfo) {
		if w.isClosed() {
			delete(w.pendingWrites, rel)
			return
		}
		cur, err := os.Stat(full)
		if err != nil || w.pendingWrites[rel] != pw {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				w.deliverError(err)
			}
			return
		}
		now := time.Now()
		if prev != nil && cur.Size() != prev.Size() {
			pw.lastChange = now
		}
		if now.Sub(pw.lastChange) >= w.opts.stabilityThreshold {
			delete(w.pendingWrites, rel)
			w.emit(pw.event, rel, cur)
			return
		}
		pw.timer = eventloop.AfterFunc(w.opts.awfPollInterval, func() { check(cur) })
	}
	pw.timer = eventloop.AfterFunc(w.opts.awfPollInterval, func() { check(nil) })
}

// emit calls the listeners of one event, then the "all" listeners. With alwaysStat an add, addDir or change
// without file information is read first; when that read fails the event is not emitted (the entry is gone), and an
// error other than a missing entry goes to the error listeners.
func (w *Watcher) emit(name, rel string, info os.FileInfo) {
	if w.isClosed() {
		return
	}
	if w.opts.alwaysStat && info == nil && (name == "add" || name == "addDir" || name == "change") {
		st, err := os.Stat(w.fullPath(rel))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				w.deliverError(err)
			}
			return
		}
		info = st
	}
	for _, fn := range append([]watchListener{}, w.listeners[name]...) {
		fn(name, rel, info)
	}
	for _, fn := range append([]watchListener{}, w.listeners["all"]...) {
		fn(name, rel, info)
	}
}

// registerTask wires the debounced, queued task run (glob-watcher's registerWatchEvent).
func (w *Watcher) registerTask(c watchConfig, run *Task) {
	if run == nil {
		return
	}
	queued, running := false, false
	var onChange func()
	runComplete := func(err error) {
		running = false
		if err != nil {
			for _, fn := range append([]func(error){}, w.errorFns...) {
				fn(err)
			}
		}
		if queued {
			queued = false
			onChange()
		}
		// released last, so a queued run keeps the command line alive
		eventloop.Unref()
	}
	onChange = func() {
		if running {
			if c.queue {
				queued = true
			}
			return
		}
		running = true
		eventloop.Ref()
		asyncDone(run, runComplete)
	}
	var timer *eventloop.Timer
	debounced := func(string, string, os.FileInfo) {
		timer.Stop()
		timer = eventloop.AfterFunc(c.delay, func() {
			timer = nil
			onChange()
		})
	}
	for _, e := range c.events {
		w.listeners[e] = append(w.listeners[e], debounced)
	}
}

// On adds a listener for a chokidar event: "add", "change", "unlink", "addDir", "unlinkDir" (fn receives the
// path, relative to the cwd option when it is set), "all" (every one of those), "ready" (path ""), or "error"
// (fn receives the error text of a failed task run or of a file system error met while watching).
// Listeners run on the loop goroutine.
func (w *Watcher) On(event string, fn func(path string)) {
	if fn == nil {
		return
	}
	if event == "error" {
		w.OnError(func(err error) { fn(err.Error()) })
		return
	}
	w.addListener(event, func(_, p string, _ os.FileInfo) { fn(p) })
}

// OnStats is On with the entry's file information, which is present for add, addDir and change events whenever the
// watcher read the entry (always with the alwaysStat option) and nil otherwise. It takes no "error" listener.
func (w *Watcher) OnStats(event string, fn func(path string, info os.FileInfo)) {
	if fn == nil || event == "error" {
		return
	}
	w.addListener(event, func(_, p string, info os.FileInfo) { fn(p, info) })
}

// OnAll adds a listener for every add, change, unlink, addDir and unlinkDir event with the event's name (the
// "all" event).
func (w *Watcher) OnAll(fn func(event, path string)) {
	if fn == nil {
		return
	}
	w.addListener("all", func(e, p string, _ os.FileInfo) { fn(e, p) })
}

func (w *Watcher) addListener(event string, fn watchListener) {
	eventloop.Dispatch(func() {
		w.listeners[event] = append(w.listeners[event], fn)
	})
}

// OnError adds a listener for the errors of failed task runs (glob-watcher emits them only when an error
// listener exists) and for file system errors met while watching, other than missing entries.
func (w *Watcher) OnError(fn func(err error)) {
	if fn == nil {
		return
	}
	eventloop.Dispatch(func() {
		w.errorFns = append(w.errorFns, fn)
	})
}

// Add watches more paths or globs (a string or []string) on a running watcher, as chokidar's add: a path that was
// unwatched or negated is watched again, and a negated glob is ignored once this Add, a later Add, or an Unwatch
// also names a path that is not negated (chokidar keeps its ignore test until then). Entries found under the new
// paths are reported as add and addDir events unless ignoreInitial is set; ready is not emitted again. Adding to a
// closed watcher does nothing. The error reports globs that are not strings.
func (w *Watcher) Add(globs any) error {
	list, err := globList(globs)
	if err != nil {
		return err
	}
	// scanMu is released before the events are dispatched, so a listener may call Add or Unwatch.
	w.scanMu.Lock()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.scanMu.Unlock()
		return nil
	}
	roots, err := w.addPaths(list, true)
	c := w.configLocked()
	w.mu.Unlock()
	if err != nil {
		w.scanMu.Unlock()
		return err
	}
	reach, errs := w.scan(c)
	w.mu.Lock()
	w.keepUnreached(reach)
	added := map[string]entryState{}
	for p, s := range reach {
		if _, ok := w.state[p]; !ok && coveredBy(roots, p) {
			w.state[p] = s
			added[p] = s
		}
	}
	w.gen++
	w.mu.Unlock()
	w.scanMu.Unlock()
	eventloop.Dispatch(func() {
		if !w.opts.ignoreInitial {
			w.deliverAdds(added)
		}
		for _, err := range errs {
			w.deliverError(err)
		}
	})
	return nil
}

// coveredBy reports whether one of roots watches p.
func coveredBy(roots []watchRoot, p string) bool {
	for _, r := range roots {
		if p != r.path && !strings.HasPrefix(p, strings.TrimSuffix(r.path, "/")+"/") {
			continue
		}
		if !r.isGlob || r.matcher.Index(p) >= 0 || r.matchesDirParts(p) {
			return true
		}
	}
	return false
}

// Unwatch stops watching paths or globs (a string or []string), as chokidar's unwatch: a relative path that is not
// itself watched is resolved against the cwd option (or the working directory), the path is ignored from then on
// (with everything below it when it is a watched folder) and no event reports it any more. Files already watched
// below an unwatched folder, or matched by an unwatched glob, keep reporting changes but not their removal, as in
// the original. Unwatching on a closed watcher does nothing. The error reports globs that are not strings, or a
// working directory that cannot be read.
func (w *Watcher) Unwatch(globs any) error {
	list, err := globList(globs)
	if err != nil {
		return err
	}
	w.scanMu.Lock()
	w.mu.Lock()
	c, err := w.unwatchLocked(list)
	w.mu.Unlock()
	if err != nil || c == nil {
		w.scanMu.Unlock()
		return err
	}
	reach, errs := w.scan(*c)
	w.mu.Lock()
	w.keepUnreached(reach)
	w.gen++
	w.mu.Unlock()
	w.scanMu.Unlock()
	eventloop.Dispatch(func() {
		for _, err := range errs {
			w.deliverError(err)
		}
	})
	return nil
}

// unwatchLocked ignores the unwatched paths and forgets them, under mu. It returns the configuration the
// following scan uses, nil when the watcher is closed.
func (w *Watcher) unwatchLocked(list []string) (*scanConfig, error) {
	if w.closed {
		return nil, nil
	}
	for _, p := range list {
		p = nodepath.Normalize(p)
		if _, watched := w.state[p]; !nodepath.IsAbs(p) && !watched {
			abs, err := nodepath.Resolve(w.cwd, p)
			if err != nil {
				return nil, err
			}
			p = abs
		}
		s, watched := w.state[p]
		delete(w.state, p)
		delete(w.kept, p)
		if err := w.ignorePath(p); err != nil {
			return nil, err
		}
		if watched && s.dir && nodepath.IsAbs(p) {
			if err := w.ignorePath(p + "/**"); err != nil {
				return nil, err
			}
		}
	}
	if err := w.applyDeferred(); err != nil {
		return nil, err
	}
	w.gen++
	c := w.configLocked()
	c.kept = nil
	return &c, nil
}

// keepUnreached updates the state after the watched paths or ignored paths changed: files no scan reaches any more
// stay watched (they keep reporting changes, as chokidar keeps their watchers open), folders are forgotten. The
// caller holds mu.
func (w *Watcher) keepUnreached(reach map[string]entryState) {
	for p, s := range w.state {
		if _, ok := reach[p]; ok {
			continue
		}
		if s.dir {
			delete(w.state, p)
			continue
		}
		w.kept[p] = true
	}
}

func (w *Watcher) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *Watcher) ref() {
	w.refed = true
	eventloop.Ref()
}

// stopPolling ends the polling goroutine without touching the event loop; the loop calls it when it halts.
func (w *Watcher) stopPolling() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.stop)
	}
}

// Close stops watching. A task run already scheduled by the debounce still happens, as in the original. Close
// returns the first file system error met while watching that no error listener received (nil otherwise);
// closing twice is allowed.
func (w *Watcher) Close() error {
	w.mu.Lock()
	unreported := w.unreported
	if w.closed {
		w.mu.Unlock()
		return unreported
	}
	w.closed = true
	close(w.stop)
	w.mu.Unlock()
	eventloop.Dispatch(func() {
		if w.refed {
			w.refed = false
			eventloop.Unref()
		}
	})
	return unreported
}

// stateOf extracts what the scanner compares from a stat result.
func stateOf(info os.FileInfo) entryState {
	s := entryState{dir: info.IsDir(), size: info.Size(), mtime: info.ModTime(), mode: info.Mode(), info: info}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.ino = uint64(st.Ino)
	}
	return s
}

// deliverError passes a file system error of a scan to the error listeners (chokidar's "error" event). With no
// listener the first such error is kept for Close to return.
func (w *Watcher) deliverError(err error) {
	if w.isClosed() {
		return
	}
	if len(w.errorFns) == 0 {
		w.mu.Lock()
		if w.unreported == nil {
			w.unreported = err
		}
		w.mu.Unlock()
		return
	}
	for _, fn := range append([]func(error){}, w.errorFns...) {
		fn(err)
	}
}
