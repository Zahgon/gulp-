package cli

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	gulp "github.com/gulpjs/gulp"
)

// taskInfo is what get-task.js reports for a top-level task.
type taskInfo struct {
	description string
	flags       map[string]string
	hasFlags    bool
}

// lookupTask follows gulp-cli's getTask: the registered task's description and flags, falling back to the function
// it wraps.
func lookupTask(g *gulp.Gulp, name string) taskInfo {
	var info taskInfo
	t := g.GetTask(name)
	if t == nil {
		return info
	}
	orig := t.Unwrap()
	info.description = t.Description
	if info.description == "" && orig != nil {
		info.description = orig.Description
	}
	info.flags = t.Flags
	if info.flags == nil && orig != nil {
		info.flags = orig.Flags
	}
	info.hasFlags = info.flags != nil
	return info
}

// treeLine is one printed line of the task tree.
type treeLine struct {
	label string
	width int
	desc  string
}

// taskTree prints gulp-cli's --tasks tree (lib/shared/log/tasks.js).
type taskTree struct {
	c         chalk
	maxDepth  float64
	compacted map[*gulp.Node]bool
	g         *gulp.Gulp
}

// logTasks returns the lines --tasks prints.
func logTasks(g *gulp.Gulp, tree *gulp.Node, f *flags, c chalk) []string {
	if truthyValue(f.sortTasks) {
		sortNodes(tree.Nodes)
	}
	t := &taskTree{c: c, g: g, maxDepth: 50, compacted: map[*gulp.Node]bool{}}
	if d, ok := f.tasksDepth.(float64); ok {
		t.maxDepth = d
		if d < 1 {
			t.maxDepth = 1
		}
	}
	if truthyValue(f.compactTasks) {
		for _, n := range tree.Nodes {
			t.compacted[n] = true
		}
	}
	lines := []treeLine{{label: tree.Label}}
	maxWidth := 0
	for i, n := range tree.Nodes {
		w := t.createTreeLines(n, &lines, 1, "", i == len(tree.Nodes)-1)
		maxWidth = max(maxWidth, w)
	}
	var out []string
	for _, l := range lines {
		s := l.label
		if l.desc != "" {
			s += strings.Repeat(" ", max(maxWidth-l.width, 0)) + "  " + l.desc
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sortNodes sorts the top-level nodes by label with compareByLabel (JavaScript string order).
func sortNodes(nodes []*gulp.Node) {
	sort.SliceStable(nodes, func(a, b int) bool {
		return compareUTF16(nodes[a].Label, nodes[b].Label) < 0
	})
}

// compareUTF16 compares strings by UTF-16 code units, JavaScript's relational string order.
func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	return len(ua) - len(ub)
}

func (t *taskTree) isLeaf(n *gulp.Node, depth int) bool {
	switch {
	case float64(depth) >= t.maxDepth:
		return true
	case depth > 1 && t.compacted[n]:
		return true
	case len(n.Nodes) == 0:
		return true
	}
	return false
}

func (t *taskTree) createTreeLines(n *gulp.Node, lines *[]treeLine, depth int, bars string, isLast bool) int {
	var info taskInfo
	if depth == 1 {
		info = lookupTask(t.g, n.Label)
	}
	leaf := t.isLeaf(n, depth)
	width := t.addTaskToLines(n.Label, info, bars, lines, isLast, leaf)
	if !leaf {
		if isLast {
			bars += " "
		} else {
			bars += t.c.paint(styleWhite, "│")
		}
		bars += " "
		for i, child := range n.Nodes {
			t.createTreeLines(child, lines, depth+1, bars, i == len(n.Nodes)-1)
		}
	}
	return width
}

// addTaskToLines adds a task's line and, for a top-level task, its flag lines (sorted by name as
// Object.entries(flags).sort(flagSorter) orders them); it returns the widest label.
func (t *taskTree) addTaskToLines(label string, info taskInfo, bars string, lines *[]treeLine, isLast, leaf bool) int {
	taskBars := bars
	if isLast {
		taskBars += t.c.paint(styleWhite, "└")
	} else {
		taskBars += t.c.paint(styleWhite, "├")
	}
	taskBars += t.c.paint(styleWhite, "─")
	if leaf {
		taskBars += t.c.paint(styleWhite, "─")
	} else {
		taskBars += t.c.paint(styleWhite, "┬")
	}
	taskBars += " "
	line := treeLine{label: taskBars + t.c.paint(styleCyan, label)}
	line.width = stringWidth(line.label)
	if info.description != "" {
		line.desc = t.c.paint(styleWhite, info.description)
	}
	*lines = append(*lines, line)
	maxWidth := line.width
	if !info.hasFlags {
		return maxWidth
	}
	flagBars := bars
	if isLast {
		flagBars += " "
	} else {
		flagBars += t.c.paint(styleWhite, "│")
	}
	flagBars += " "
	if leaf {
		flagBars += " "
	} else {
		flagBars += t.c.paint(styleWhite, "│")
	}
	flagBars += " "
	names := make([]string, 0, len(info.flags))
	for name := range info.flags {
		names = append(names, name)
	}
	sort.Slice(names, func(a, b int) bool { return compareUTF16(names[a], names[b]) < 0 })
	for _, name := range names {
		if name == "" {
			continue
		}
		fl := treeLine{label: flagBars + t.c.paint(styleMagenta, name)}
		fl.width = stringWidth(fl.label)
		maxWidth = max(maxWidth, fl.width)
		if d := info.flags[name]; d != "" {
			fl.desc = t.c.paint(styleWhite, "…"+d)
		}
		*lines = append(*lines, fl)
	}
	return maxWidth
}

// tasksSimple is --tasks-simple: the top-level task names, one per line.
func tasksSimple(tree *gulp.Node) string {
	labels := make([]string, len(tree.Nodes))
	for i, n := range tree.Nodes {
		labels[i] = n.Label
	}
	return jsTrim(strings.Join(labels, "\n"))
}

// tasksJSON follows JSON.stringify(copyTree(tree, opts)) for --tasks-json.
func tasksJSON(tree *gulp.Node, f *flags) string {
	depth := math.NaN()
	hasDepth := false
	if d, ok := f.tasksDepth.(float64); ok {
		hasDepth = true
		depth = d
		if d < 1 {
			depth = 1
		}
	}
	unlimited := !hasDepth || depth == 0 || math.IsNaN(depth)
	compact := truthyValue(f.compactTasks)
	var b strings.Builder
	b.WriteString(`{"label":`)
	b.WriteString(jsonString(tree.Label))
	b.WriteString(`,"nodes":[`)
	for i, n := range tree.Nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		var children func(*strings.Builder)
		switch {
		case compact:
			children = func(sb *strings.Builder) { writeCompact(sb, n.Nodes) }
		case unlimited || depth > 1:
			children = func(sb *strings.Builder) { writeRecursive(sb, n.Nodes, depth, unlimited, 2) }
		}
		writeNode(&b, n, children)
	}
	b.WriteString("]}")
	return b.String()
}

// writeNode writes a copied node: its own keys, then nodes filled by children (empty when nil).
func writeNode(b *strings.Builder, n *gulp.Node, children func(*strings.Builder)) {
	b.WriteString(`{"label":`)
	b.WriteString(jsonString(n.Label))
	if n.Type != "" {
		b.WriteString(`,"type":`)
		b.WriteString(jsonString(n.Type))
	}
	if n.Branch {
		b.WriteString(`,"branch":true`)
	}
	b.WriteString(`,"nodes":[`)
	if children != nil {
		children(b)
	}
	b.WriteString("]}")
}

func writeCompact(b *strings.Builder, nodes []*gulp.Node) {
	for i, child := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		var children func(*strings.Builder)
		if child.Branch {
			grand := child.Nodes
			children = func(sb *strings.Builder) { writeCompact(sb, grand) }
		}
		writeNode(b, child, children)
	}
}

func writeRecursive(b *strings.Builder, nodes []*gulp.Node, maxDepth float64, unlimited bool, nowDepth int) {
	for i, child := range nodes {
		if i > 0 {
			b.WriteByte(',')
		}
		var children func(*strings.Builder)
		if unlimited || maxDepth > float64(nowDepth) {
			grand := child.Nodes
			children = func(sb *strings.Builder) { writeRecursive(sb, grand, maxDepth, unlimited, nowDepth+1) }
		}
		writeNode(b, child, children)
	}
}

// jsonString follows JSON.stringify for a string.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				hex := strconv.FormatInt(int64(r), 16)
				if len(hex) < 2 {
					hex = "0" + hex
				}
				b.WriteString(hex)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
