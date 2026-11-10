package gulp

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/gulpjs/gulp/internal/eventloop"
	"github.com/gulpjs/gulp/internal/jsnum"
)

// Node is a node of the task tree (undertaker's tree()).
type Node struct {
	Label  string  `json:"label"`
	Type   string  `json:"type,omitempty"`
	Branch bool    `json:"branch,omitempty"`
	Nodes  []*Node `json:"nodes"`
}

// Tree returns the task tree. With deep false each node carries only a task's label (the original returns the
// label strings); with deep true the nodes are the full trees of the registered tasks, compositions included.
func (g *Gulp) Tree(deep bool) *Node {
	root := &Node{Label: "Tasks", Nodes: []*Node{}}
	for _, nt := range jsObjectOrder(g.Registry().Tasks()) {
		var tree *Node
		if nt.Task != nil {
			if m := nt.Task.metadata(); m != nil {
				tree = m.tree
			}
		}
		if tree == nil {
			tree = &Node{Label: nt.Name, Type: "task", Nodes: []*Node{}}
		}
		if deep {
			root.Nodes = append(root.Nodes, tree)
		} else {
			root.Nodes = append(root.Nodes, &Node{Label: tree.Label, Nodes: []*Node{}})
		}
	}
	return root
}

// TaskNotDefinedError is the error Series and Parallel return for a name the registry does not know. Similar
// lists the registered names within the original's Levenshtein distance (empty for numeric names).
type TaskNotDefinedError struct {
	Task    string
	Similar []string
}

// Error returns "Task never defined: <name>", with " - did you mean? <names>" when similar names exist.
func (e *TaskNotDefinedError) Error() string {
	if len(e.Similar) > 0 {
		return "Task never defined: " + e.Task + " - did you mean? " + strings.Join(e.Similar, ", ")
	}
	return "Task never defined: " + e.Task
}

// SettleError is the list of errors a settled composition (--continue, UNDERTAKER_SETTLE=true) completes with:
// one entry per failed child, in composition order.
type SettleError struct {
	Errors []error
}

// Error returns the list as JavaScript's String(array) prints an array of errors: the entries' "Error: <message>"
// texts joined with commas.
func (e *SettleError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		switch v := err.(type) {
		case nil:
		case *SettleError:
			parts[i] = v.Error()
		case *AssertionError:
			parts[i] = "AssertionError [ERR_ASSERTION]: " + v.Message
		default:
			if msg := err.Error(); msg != "" {
				parts[i] = "Error: " + msg
			} else {
				parts[i] = "Error"
			}
		}
	}
	return strings.Join(parts, ",")
}

// Series composes tasks to run one after another (undertaker's series). Each argument is a registered task name,
// a *Task or a TaskFunc; slices of those are flattened one level. A failing task stops the series unless the
// instance settles. The error is a *TaskNotDefinedError for a name no task is registered under, or the assertion
// error "One or more tasks should be combined using series or parallel" when no task is given.
func (g *Gulp) Series(tasks ...any) (*Task, error) {
	return g.compose("<series>", "series", tasks)
}

// Parallel composes tasks to start together (undertaker's parallel). Arguments and errors are as for Series; the
// first failing task fails the composition unless the instance settles.
func (g *Gulp) Parallel(tasks ...any) (*Task, error) {
	return g.compose("<parallel>", "parallel", tasks)
}

func (g *Gulp) compose(label, kind string, args []any) (*Task, error) {
	fns, err := g.normalizeArgs(args)
	if err != nil {
		return nil, err
	}
	nodes := make([]*Node, len(fns))
	for i, fn := range fns {
		nodes[i] = buildTree(fn)
	}
	fnName := kind
	if g.settled() {
		fnName = "settle" + strings.ToUpper(kind[:1]) + kind[1:]
	}
	t := &Task{fnName: fnName}
	t.Fn = func(done Done) {
		eventloop.Dispatch(func() {
			run := &composition{g: g, settle: g.settled(), fns: fns, done: done}
			if kind == "series" {
				run.series()
			} else {
				run.parallel()
			}
		})
	}
	t.setMetadata(&taskMeta{
		name:   label,
		branch: true,
		tree:   &Node{Label: label, Type: "function", Branch: true, Nodes: nodes},
	})
	return t, nil
}

// buildTree returns a function's tree node, creating its metadata on first use (undertaker's buildTree).
func buildTree(t *Task) *Node {
	metaMu.Lock()
	defer metaMu.Unlock()
	if t.meta != nil {
		return t.meta.tree
	}
	name := t.DisplayName
	if name == "" {
		name = t.fnName
	}
	if name == "" {
		name = "<anonymous>"
	}
	t.meta = &taskMeta{name: name, tree: &Node{Label: name, Type: "function", Nodes: []*Node{}}}
	return t.meta.tree
}

// normalizeArgs flattens the arguments one level and resolves names through the registry.
func (g *Gulp) normalizeArgs(args []any) ([]*Task, error) {
	var flat []any
	for _, a := range args {
		switch v := a.(type) {
		case []any:
			flat = append(flat, v...)
		case []string:
			for _, s := range v {
				flat = append(flat, s)
			}
		case []*Task:
			for _, t := range v {
				flat = append(flat, t)
			}
		case []TaskFunc:
			for _, f := range v {
				flat = append(flat, f)
			}
		default:
			flat = append(flat, a)
		}
	}
	if len(flat) == 0 {
		return nil, assertionError("One or more tasks should be combined using series or parallel")
	}
	out := make([]*Task, 0, len(flat))
	for _, a := range flat {
		t, err := g.getFunction(a)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// getFunction resolves one argument: functions pass through, anything else is looked up by its JavaScript
// string form; only string names get "did you mean" suggestions.
func (g *Gulp) getFunction(a any) (*Task, error) {
	switch v := a.(type) {
	case *Task:
		if v != nil && v.Fn != nil {
			return v, nil
		}
	case TaskFunc:
		if v != nil {
			return &Task{Fn: v}, nil
		}
	case func(Done):
		if v != nil {
			return &Task{Fn: v}, nil
		}
	}
	key, isString := jsKey(a)
	if t := g.Registry().Get(key); t != nil {
		return t, nil
	}
	err := &TaskNotDefinedError{Task: key}
	if isString {
		err.Similar = g.similarTasks(key)
	}
	return nil, err
}

// jsKey returns String(v) as a property key, and whether v is a JavaScript string.
func jsKey(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return jsnum.Format(x), false
	case float32:
		return jsnum.Format(float64(x)), false
	case int:
		return jsnum.Format(float64(x)), false
	case int64:
		return jsnum.Format(float64(x)), false
	case int32:
		return jsnum.Format(float64(x)), false
	case uint:
		return jsnum.Format(float64(x)), false
	case uint64:
		return jsnum.Format(float64(x)), false
	case bool:
		if x {
			return "true", false
		}
		return "false", false
	case nil:
		return "undefined", false
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i], _ = jsKey(e)
			}
		}
		return strings.Join(parts, ","), false
	case *Task, TaskFunc, func(Done):
		return "null", false
	}
	return fmt.Sprint(v), false
}

// similarTasks lists registered names close to name (undertaker's normalizeArgs similarTasks).
func (g *Gulp) similarTasks(name string) []string {
	var out []string
	query := utf16.Encode([]rune(name))
	for _, nt := range jsObjectOrder(g.Registry().Tasks()) {
		units := utf16.Encode([]rune(nt.Name))
		allowed := len(units)*4/10 + 1
		if levenshtein(units, query) < allowed {
			out = append(out, nt.Name)
		}
	}
	return out
}

// levenshtein follows fast-levenshtein's get over UTF-16 code units.
func levenshtein(a, b []uint16) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	next := 0
	for i := range a {
		next = i + 1
		for j := range b {
			cur := next
			cost := 1
			if a[i] == b[j] {
				cost = 0
			}
			next = prev[j] + cost
			if ins := cur + 1; next > ins {
				next = ins
			}
			if del := prev[j+1] + 1; next > del {
				next = del
			}
			prev[j] = cur
		}
		prev[len(b)] = next
	}
	return next
}
