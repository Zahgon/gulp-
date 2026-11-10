package cli

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/gulpjs/gulp/internal/nodepath"
)

// configName is the file gulp-cli's liftoff instance looks for as ".gulp" plus an extension. JavaScript config
// files (.gulp.js and its variants) cannot be evaluated in Go, so the JSON form is read instead.
const configName = ".gulp.json"

// defaultGulpfile is the name searched for when the caller has no gulpfile path.
const defaultGulpfile = "gulpfile.js"

// environment is the part of liftoff's buildEnvironment the command line needs.
type environment struct {
	cwd        string
	configPath string           // "" when no gulpfile was found
	config     []map[string]any // [config found upward from the cwd, config in the home directory]; {} when absent
}

// buildEnvironment resolves the working directory, the config files and the gulpfile as liftoff 5 does for
// gulp-cli. gulpfile is the default gulpfile the caller provides (what liftoff finds by searching upward).
func buildEnvironment(f *flags, gulpfile, home string) (*environment, error) {
	processCwd, err := nodepath.Cwd()
	if err != nil {
		return nil, err
	}
	optCwd, hasCwd := f.cwd.(string)
	if !hasCwd || optCwd == "" {
		hasCwd = false
	}
	optGulpfile, gulpfileIsString := f.gulpfile.(string)
	explicitGulpfile := truthyValue(f.gulpfile)

	// find_cwd.js: --cwd wins, otherwise the folder of a --gulpfile string (even an empty one).
	cwd := processCwd
	switch {
	case hasCwd:
		cwd = nodepath.ResolveIn(processCwd, optCwd)
	case gulpfileIsString:
		cwd = nodepath.Dirname(nodepath.ResolveIn(processCwd, optGulpfile))
	}

	env := &environment{cwd: cwd}
	locations := []string{findUp(cwd, configName), homeConfig(processCwd, home)}
	for _, loc := range locations {
		if loc == "" {
			env.config = append(env.config, map[string]any{})
			continue
		}
		cfg, err := loadConfig(loc, map[string]any{}, map[string]bool{})
		if err != nil {
			return nil, err
		}
		env.config = append(env.config, cfg)
	}

	// configPathOverride: the first config with a string gulpfile key.
	override := ""
	for _, cfg := range env.config {
		if s, ok := cfg["gulpfile"].(string); ok && s != "" {
			override = s
			break
		}
	}

	// find_config.js: an explicit path must exist; otherwise the gulpfile is searched upward from a cwd that
	// differs from the caller's, or the caller's gulpfile is used as found.
	var configPath string
	switch {
	case explicitGulpfile:
		if p := nodepath.ResolveIn(processCwd, optGulpfile); gulpfileIsString && exists(p) {
			configPath = p
		}
	case override != "":
		if exists(override) {
			configPath = override
		}
	case hasCwd || gulpfileIsString:
		name := nodepath.Basename(gulpfile, "")
		if gulpfile == "" {
			name = defaultGulpfile
		}
		configPath = findUp(cwd, name)
	case gulpfile != "":
		configPath = nodepath.ResolveIn(processCwd, gulpfile)
	}
	env.configPath = configPath
	if configPath != "" && !hasCwd {
		env.cwd = nodepath.Dirname(configPath)
	}
	return env, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// findUp returns dir/name for the nearest dir at or above start holding name (as liftoff searches upward).
func findUp(start, name string) string {
	dir := start
	for {
		p := nodepath.Join(dir, name)
		if exists(p) {
			return p
		}
		parent := nodepath.Dirname(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// homeConfig returns home's .gulp.json when it exists; a relative home is resolved against processCwd.
func homeConfig(processCwd, home string) string {
	p := nodepath.Join(nodepath.ResolveIn(processCwd, home), configName)
	if exists(p) {
		return p
	}
	return ""
}

// loadConfig follows liftoff's loadConfig: read the file, resolve its gulpfile key against its folder, follow
// extends (the extended file is the base, the extending file overrides it).
func loadConfig(path string, preferred map[string]any, visited map[string]bool) (map[string]any, error) {
	if visited[path] {
		return nil, errors.New("We encountered a circular extend for file: " + path + ". Please remove the recursive extends.")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("Encountered error when loading config file: " + path)
	}
	var parsed any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errors.New("Encountered error when loading config file: " + path)
	}
	cfg, ok := parsed.(map[string]any)
	if !ok {
		cfg = map[string]any{}
	}
	if s, ok := cfg["gulpfile"].(string); ok {
		cfg["gulpfile"] = nodepath.ResolveIn(nodepath.Dirname(path), s)
	}
	visited[path] = true
	if xtends, ok := cfg["extends"]; ok && truthyValue(xtends) {
		next, err := extendsPath(nodepath.Dirname(path), xtends)
		if err != nil {
			return nil, err
		}
		return loadConfig(next, cfg, visited)
	}
	merged := deepExtend(deepExtend(map[string]any{}, cfg), preferred)
	delete(merged, "extends")
	return merged, nil
}

// extendsPath locates the file an extends value names. Relative names ("./base.json") are found from the
// config's folder cwd (absolute); a name without an extension tries ".json".
func extendsPath(cwd string, xtends any) (string, error) {
	name := ""
	switch v := xtends.(type) {
	case string:
		name = v
		if !strings.HasPrefix(v, ".") {
			if nodepath.IsAbs(v) && exists(v) {
				return v, nil
			}
			return "", errors.New("Encountered error when loading config file: " + v)
		}
	case map[string]any:
		if p, ok := v["path"].(string); ok {
			name = p
		} else if n, ok := v["name"].(string); ok {
			name = n
		}
	}
	if name != "" {
		candidate := nodepath.ResolveIn(cwd, name)
		if nodepath.Extname(name) == "" {
			candidate += ".json"
		}
		if exists(candidate) {
			return candidate, nil
		}
	}
	msg := "Unable to locate one of your extends."
	if name != "" {
		msg += " Looking for file: " + nodepath.ResolveIn(cwd, name)
	}
	return "", errors.New(msg)
}

// deepExtend is extend(true, target, source): plain objects and arrays are merged recursively, other values
// replace.
func deepExtend(target map[string]any, source map[string]any) map[string]any {
	for k, v := range source {
		target[k] = extendValue(target[k], v)
	}
	return target
}

func extendValue(src, cp any) any {
	switch c := cp.(type) {
	case map[string]any:
		clone, ok := src.(map[string]any)
		if !ok {
			clone = map[string]any{}
		}
		return deepExtend(clone, c)
	case []any:
		clone, ok := src.([]any)
		if !ok {
			clone = nil
		}
		for i, v := range c {
			if i < len(clone) {
				clone[i] = extendValue(clone[i], v)
			} else {
				clone = append(clone, extendValue(nil, v))
			}
		}
		if clone == nil {
			clone = []any{}
		}
		return clone
	}
	return cp
}

// configFlags maps the config keys gulp-cli reads (lib/shared/config/cli-flags.js) to flag names.
var configFlags = map[string]string{
	"flags.silent":       "silent",
	"flags.continue":     "continue",
	"flags.series":       "series",
	"flags.logLevel":     "logLevel",
	"flags.compactTasks": "compactTasks",
	"flags.tasksDepth":   "tasksDepth",
	"flags.sortTasks":    "sortTasks",
}

// mergeConfigFlags applies the config's flags to the command line flags that are undefined (copy-props with
// the defaults converter).
func mergeConfigFlags(f *flags, cfg map[string]any) {
	pending := map[string]string{}
	for k, v := range configFlags {
		pending[k] = v
	}
	var walk func(obj map[string]any, prefix string)
	walk = func(obj map[string]any, prefix string) {
		for k, v := range obj {
			chain := k
			if prefix != "" {
				chain = prefix + "." + k
			}
			if m, ok := v.(map[string]any); ok {
				walk(m, chain)
				continue
			}
			name, ok := pending[chain]
			if !ok {
				continue
			}
			delete(pending, chain)
			f.setIfUndefined(name, v)
		}
	}
	walk(cfg, "")
}
