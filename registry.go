package gulp

import (
	"sort"
	"strconv"
	"sync"
)

// NamedTask is one entry of a registry listing.
type NamedTask struct {
	Name string
	Task *Task
}

// Registry stores tasks by name (undertaker-registry's interface). Tasks lists the entries in the order a
// JavaScript object holding them enumerates its keys.
type Registry interface {
	// Init is called once the registry is installed on g with SetRegistry, after the tasks g already had were
	// copied into it with Set.
	Init(g *Gulp)
	// Get returns the task registered under name, or nil when there is none.
	Get(name string) *Task
	// Set registers task under name and returns the task to keep in the gulp metadata: the one it stored, or nil
	// to keep task itself.
	Set(name string, task *Task) *Task
	// Tasks lists every registered task with its name, in registration order with integer-like names first.
	Tasks() []NamedTask
}

// defaultRegistry follows undertaker-registry 2: a plain object keyed by task name.
type defaultRegistry struct {
	mu    sync.Mutex
	order []string
	tasks map[string]*Task
}

// NewDefaultRegistry returns an empty registry that keeps tasks in registration order with integer-like names
// first, as a JavaScript object lists its keys.
func NewDefaultRegistry() Registry {
	return &defaultRegistry{tasks: map[string]*Task{}}
}

// Init does nothing, as in undertaker-registry.
func (r *defaultRegistry) Init(*Gulp) {}

// Get returns the task stored under name or nil.
func (r *defaultRegistry) Get(name string) *Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tasks[name]
}

// Set stores task under name, replacing an existing entry in place, and returns it.
func (r *defaultRegistry) Set(name string, task *Task) *Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tasks[name]; !ok {
		r.order = append(r.order, name)
	}
	r.tasks[name] = task
	return task
}

// Tasks returns every entry in JavaScript object key order.
func (r *defaultRegistry) Tasks() []NamedTask {
	r.mu.Lock()
	out := make([]NamedTask, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, NamedTask{Name: name, Task: r.tasks[name]})
	}
	r.mu.Unlock()
	return jsObjectOrder(out)
}

// arrayIndex reports whether key is a canonical array index ("0", "17", but not "01" or "4294967295"), the
// keys a JavaScript object enumerates first, in ascending numeric order.
func arrayIndex(key string) (uint64, bool) {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil || n >= 1<<32-1 {
		return 0, false
	}
	return n, true
}

// jsObjectOrder returns entries as a JavaScript object built from them enumerates them: array-index keys first
// in ascending order, then the other keys in insertion order; a repeated key keeps its first position and its
// last value.
func jsObjectOrder(entries []NamedTask) []NamedTask {
	type indexed struct {
		n     uint64
		entry NamedTask
	}
	var ints []indexed
	var strs []NamedTask
	pos := map[string]int{}
	for _, e := range entries {
		if i, ok := pos[e.Name]; ok {
			if i >= 0 {
				strs[i].Task = e.Task
			} else {
				ints[-i-1].entry.Task = e.Task
			}
			continue
		}
		if n, ok := arrayIndex(e.Name); ok {
			ints = append(ints, indexed{n, e})
			pos[e.Name] = -len(ints)
			continue
		}
		strs = append(strs, e)
		pos[e.Name] = len(strs) - 1
	}
	sort.SliceStable(ints, func(a, b int) bool { return ints[a].n < ints[b].n })
	out := make([]NamedTask, 0, len(ints)+len(strs))
	for _, x := range ints {
		out = append(out, x.entry)
	}
	return append(out, strs...)
}
