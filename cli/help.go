package cli

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The help text is yargs 16's showHelp output for gulp-cli's options, laid out by an implementation of the parts of
// cliui 7.0.4, wrap-ansi 7.0.0 and string-width 4.2.3 it uses, for the 80 column width yargs picks when stdout
// is not a terminal.

const helpWidth = 80

// ansiRe follows ansi-regex 5.0.1 (what strip-ansi 6 removes).
var ansiRe = regexp.MustCompile("[\x1b\u009b][[\\]()#;?]*(?:(?:(?:(?:;[-a-zA-Z\\d\\/#&.:=?%@~_]+)*|[a-zA-Z\\d]+(?:;[-a-zA-Z\\d\\/#&.:=?%@~_]*)*)?\x07)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PR-TZcf-ntqry=><~]))")

func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// stringWidth follows string-width 4.2.3: columns of s once ANSI codes are removed, emoji count 2, East Asian wide
// characters 2, control and combining characters 0.
func stringWidth(s string) int {
	if s == "" {
		return 0
	}
	s = stripAnsi(s)
	if s == "" {
		return 0
	}
	s = replaceEmoji(s)
	width := 0
	for _, r := range s {
		switch {
		case r <= 0x1f || (r >= 0x7f && r <= 0x9f):
			continue
		case r >= 0x300 && r <= 0x36f:
			continue
		}
		if isFullwidth(r) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

// isFullwidth is is-fullwidth-code-point 3.0.0.
func isFullwidth(r rune) bool {
	if r < 0x1100 {
		return false
	}
	return r <= 0x115f ||
		r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0x3247 && r != 0x303f) ||
		(r >= 0x3250 && r <= 0x4dbf) ||
		(r >= 0x4e00 && r <= 0xa4c6) ||
		(r >= 0xa960 && r <= 0xa97c) ||
		(r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) ||
		(r >= 0xfe10 && r <= 0xfe19) ||
		(r >= 0xfe30 && r <= 0xfe6b) ||
		(r >= 0xff01 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1b000 && r <= 0x1b001) ||
		(r >= 0x1f200 && r <= 0x1f251) ||
		(r >= 0x20000 && r <= 0x3fffd)
}

// wrapAnsi follows wrap-ansi 7 with {hard: true}: every line of s is word wrapped to columns, long words are broken,
// and SGR colour codes open at a line break are closed before it and reopened after it.
func wrapAnsi(s string, columns int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = wrapLine(line, columns)
	}
	return strings.Join(lines, "\n")
}

// wrapLine follows wrap-ansi's exec for one line.
func wrapLine(s string, columns int) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	words := strings.Split(s, " ")
	lengths := make([]int, len(words))
	for i, w := range words {
		lengths[i] = stringWidth(w)
	}
	rows := []string{""}
	for index, word := range words {
		rows[len(rows)-1] = strings.TrimLeft(rows[len(rows)-1], " \t\n\v\f\r")
		rowLength := stringWidth(rows[len(rows)-1])
		if index != 0 && rowLength > 0 {
			rows[len(rows)-1] += " "
			rowLength++
		}
		if lengths[index] > columns {
			remaining := columns - rowLength
			breaksThisLine := 1 + floorDiv(lengths[index]-remaining-1, columns)
			breaksNextLine := floorDiv(lengths[index]-1, columns)
			if breaksNextLine < breaksThisLine {
				rows = append(rows, "")
			}
			rows = wrapWord(rows, word, columns)
			continue
		}
		if rowLength+lengths[index] > columns && rowLength > 0 && lengths[index] > 0 {
			rows = append(rows, "")
		}
		rows[len(rows)-1] += word
	}
	for i, r := range rows {
		rows[i] = trimVisibleRight(r)
	}
	return reapplyCodes(strings.Join(rows, "\n"))
}

func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// wrapWord breaks a word longer than the column count across rows.
func wrapWord(rows []string, word string, columns int) []string {
	chars := []rune(word)
	inEscape := false
	visible := stringWidth(stripAnsi(rows[len(rows)-1]))
	for index, ch := range chars {
		w := stringWidth(string(ch))
		if visible+w <= columns {
			rows[len(rows)-1] += string(ch)
		} else {
			rows = append(rows, string(ch))
			visible = 0
		}
		if ch == '\x1b' || ch == '\u009b' {
			inEscape = true
		}
		if inEscape {
			if ch == 'm' {
				inEscape = false
			}
			continue
		}
		visible += w
		if visible == columns && index < len(chars)-1 {
			rows = append(rows, "")
			visible = 0
		}
	}
	if visible == 0 && len(rows[len(rows)-1]) > 0 && len(rows) > 1 {
		last := rows[len(rows)-1]
		rows = rows[:len(rows)-1]
		rows[len(rows)-1] += last
	}
	return rows
}

// trimVisibleRight removes trailing space-separated words of zero visible width.
func trimVisibleRight(s string) string {
	words := strings.Split(s, " ")
	last := len(words)
	for last > 0 && stringWidth(words[last-1]) == 0 {
		last--
	}
	if last == len(words) {
		return s
	}
	return strings.Join(words[:last], " ") + strings.Join(words[last:], "")
}

var sgrRe = regexp.MustCompile(`^\[(\d+)m`)

// closeCodes maps an SGR open code to its close code (ansi-styles' codes map).
var closeCodes = map[int]int{
	1: 22, 2: 22, 3: 23, 4: 24, 7: 27, 8: 28, 9: 29,
	30: 39, 31: 39, 32: 39, 33: 39, 34: 39, 35: 39, 36: 39, 37: 39, 90: 39, 91: 39, 92: 39, 93: 39, 94: 39,
	95: 39, 96: 39, 97: 39,
	40: 49, 41: 49, 42: 49, 43: 49, 44: 49, 45: 49, 46: 49, 47: 49, 100: 49, 101: 49, 102: 49, 103: 49,
	104: 49, 105: 49, 106: 49, 107: 49,
}

// reapplyCodes closes the open SGR code before each line break and reopens it after, as wrap-ansi's final pass
// does.
func reapplyCodes(pre string) string {
	var b strings.Builder
	code := 0
	for i, ch := range pre {
		b.WriteRune(ch)
		if ch == '\x1b' || ch == '\u009b' {
			if m := sgrRe.FindStringSubmatch(pre[i+utf8.RuneLen(ch):]); m != nil {
				n, err := strconv.Atoi(m[1])
				if err == nil {
					if n == 39 {
						code = 0
					} else {
						code = n
					}
				}
			}
		}
		closeCode, known := closeCodes[code]
		next := i + utf8.RuneLen(ch)
		switch {
		case next < len(pre) && pre[next] == '\n':
			if code != 0 && known {
				b.WriteString("\x1b[" + strconv.Itoa(closeCode) + "m")
			}
		case ch == '\n':
			if code != 0 && known {
				b.WriteString("\x1b[" + strconv.Itoa(code) + "m")
			}
		}
	}
	return b.String()
}

// column is a cliui column.
type column struct {
	text    string
	width   int    // 0 when unset
	padding [4]int // top, right, bottom, left
	align   string // "" or "right"
}

// uiRow is a cliui row; span rows may share their last line with the next row.
type uiRow struct {
	cols []column
	span bool
}

// ui is the subset of cliui yargs' help uses, with wrapping on.
type ui struct {
	width int
	rows  []uiRow
}

type uiLine struct {
	text   string
	span   bool
	hidden bool
}

// div adds a row; a single string containing a line break is split into rows (cliui's layout DSL).
func (u *ui) div(cols ...column) {
	u.rows = append(u.rows, uiRow{cols: cols})
}

// divText adds a row from text the way cliui's div(string) does.
func (u *ui) divText(text string) {
	if strings.ContainsAny(text, "\t\n") {
		for _, row := range strings.Split(text, "\n") {
			var cols []column
			for _, cell := range strings.Split(row, "\t") {
				cols = append(cols, column{text: strings.TrimSpace(cell), padding: measurePadding(cell)})
			}
			u.rows = append(u.rows, uiRow{cols: cols})
		}
		return
	}
	u.rows = append(u.rows, uiRow{cols: []column{{text: text, padding: measurePadding(text)}}})
}

func (u *ui) span(cols ...column) {
	u.rows = append(u.rows, uiRow{cols: cols, span: true})
}

func measurePadding(s string) [4]int {
	plain := stripAnsi(s)
	right := len(plain) - len(strings.TrimRight(plain, " \t\n\v\f\r"))
	left := len(plain) - len(strings.TrimLeft(plain, " \t\n\v\f\r"))
	return [4]int{0, right, 0, left}
}

// String renders the rows, dropping lines a following span row was merged into.
func (u *ui) String() string {
	var lines []*uiLine
	for _, row := range u.rows {
		lines = u.rowToString(row, lines)
	}
	var out []string
	for _, l := range lines {
		if !l.hidden {
			out = append(out, l.text)
		}
	}
	return strings.Join(out, "\n")
}

func negatePadding(c column) int {
	return c.width - c.padding[3] - c.padding[1]
}

func (u *ui) columnWidths(row uiRow) []int {
	unset := len(row.cols)
	remaining := u.width
	widths := make([]int, len(row.cols))
	for i, c := range row.cols {
		if c.width > 0 {
			unset--
			remaining -= c.width
			widths[i] = c.width
		}
	}
	unsetWidth := 0
	if unset > 0 {
		unsetWidth = floorDiv(remaining, unset)
	}
	for i, c := range row.cols {
		if widths[i] == 0 {
			minWidth := 1 + c.padding[3] + c.padding[1]
			widths[i] = max(unsetWidth, minWidth)
		}
	}
	return widths
}

func (u *ui) rasterize(row uiRow) [][]string {
	widths := u.columnWidths(row)
	var rrows [][]string
	for c := range row.cols {
		row.cols[c].width = widths[c]
		col := row.cols[c]
		wrapped := strings.Split(wrapAnsi(col.text, negatePadding(col)), "\n")
		for i := 0; i < col.padding[0]; i++ {
			wrapped = append([]string{""}, wrapped...)
		}
		for i := 0; i < col.padding[2]; i++ {
			wrapped = append(wrapped, "")
		}
		for r, str := range wrapped {
			if r >= len(rrows) {
				rrows = append(rrows, nil)
			}
			for len(rrows[r]) < c {
				rrows[r] = append(rrows[r], "")
			}
			rrows[r] = append(rrows[r], str)
		}
	}
	return rrows
}

func (u *ui) rowToString(row uiRow, lines []*uiLine) []*uiLine {
	for r, rrow := range u.rasterize(row) {
		str := ""
		for c, cell := range rrow {
			col := row.cols[c]
			wrapWidth := negatePadding(col)
			ts := cell
			if w := stringWidth(cell); wrapWidth > w {
				ts += strings.Repeat(" ", wrapWidth-w)
			}
			if col.align == "right" {
				ts = alignRight(ts, wrapWidth)
				if w := stringWidth(ts); w < wrapWidth {
					ts += strings.Repeat(" ", max(col.width-w-1, 0))
				}
			}
			str += strings.Repeat(" ", col.padding[3]) + ts + strings.Repeat(" ", col.padding[1])
			if r == 0 && len(lines) > 0 {
				str = u.renderInline(str, lines[len(lines)-1])
			}
		}
		lines = append(lines, &uiLine{text: strings.TrimRight(str, " "), span: row.span})
	}
	return lines
}

func alignRight(s string, width int) string {
	s = strings.TrimSpace(s)
	if w := stringWidth(s); w < width {
		return strings.Repeat(" ", width-w) + s
	}
	return s
}

func (u *ui) renderInline(source string, prev *uiLine) string {
	leading := len(source) - len(strings.TrimLeft(source, " "))
	target := prev.text
	targetWidth := stringWidth(strings.TrimRight(target, " \t\n\v\f\r"))
	if !prev.span {
		return source
	}
	if leading < targetWidth {
		return source
	}
	prev.hidden = true
	return strings.TrimRight(target, " \t\n\v\f\r") + strings.Repeat(" ", leading-targetWidth) + strings.TrimLeft(source, " \t\n\v\f\r")
}

// helpText follows yargs' help() for gulp-cli's options with the default translations.
func helpText(c chalk) string {
	u := &ui{width: helpWidth}
	u.divText(messageUsage(c))
	u.div(column{})
	type entry struct {
		sw     string
		indent int
		desc   string
		extra  string
	}
	var entries []entry
	maxW := 0
	for _, o := range cliOptions {
		sw := "--" + o.key
		indent := 0
		if o.alias != "" {
			if len(o.alias) > 1 {
				sw += ", --" + o.alias
				indent = 4
			} else {
				sw = "-" + o.alias + ", " + sw
			}
		} else {
			indent = 4
		}
		extra := ""
		switch o.kind {
		case "boolean", "string", "number", "count":
			extra = "[" + o.kind + "]"
		}
		entries = append(entries, entry{sw, indent, c.paint(styleGray, flagDescriptions[o.tag]), extra})
		maxW = max(maxW, stringWidth(sw)+indent)
	}
	maxW = min(maxW, helpWidth/2)
	u.divText("Options:")
	for _, e := range entries {
		u.span(
			column{text: e.sw, width: maxW + 4, padding: [4]int{0, 2, 0, 2 + e.indent}},
			column{text: e.desc, padding: measurePadding(e.desc)},
		)
		if e.extra != "" {
			u.div(column{text: e.extra, padding: [4]int{0, 0, 0, 2}, align: "right"})
		} else {
			u.div(column{})
		}
	}
	u.div(column{})
	return strings.TrimRight(u.String(), " \t\n\v\f\r")
}

// flagDescriptions are the default FLAG_* messages of gulp-cli's translate.js (before colouring).
var flagDescriptions = map[string]string{
	"FLAG_HELP":    "Show this help.",
	"FLAG_VERSION": "Print the global and local gulp versions.",
	"FLAG_PRELOAD": "Will preload a module before running the gulpfile. " +
		"This is useful for transpilers but also has other applications.",
	"FLAG_GULPFILE": "Manually set path of gulpfile. Useful if you have multiple gulpfiles. " +
		"This will set the CWD to the gulpfile directory as well.",
	"FLAG_CWD": "Manually set the CWD. The search for the gulpfile, " +
		"as well as the relativity of all requires will be from here.",
	"FLAG_TASKS":         "Print the task dependency tree for the loaded gulpfile.",
	"FLAG_TASKS_SIMPLE":  "Print a plaintext list of tasks for the loaded gulpfile.",
	"FLAG_TASKS_JSON":    "Print the task dependency tree, in JSON format, for the loaded gulpfile.",
	"FLAG_TASKS_DEPTH":   "Specify the depth of the task dependency tree.",
	"FLAG_COMPACT_TASKS": "Reduce the output of task dependency tree by printing only top tasks and their child tasks.",
	"FLAG_SORT_TASKS":    "Will sort top tasks of task dependency tree.",
	"FLAG_COLOR":         "Will force gulp and gulp plugins to display colors, even when no color support is detected.",
	"FLAG_NO_COLOR":      "Will force gulp and gulp plugins to not display colors, even when color support is detected.",
	"FLAG_SILENT":        "Suppress all gulp logging.",
	"FLAG_CONTINUE":      "Continue execution of tasks upon failure.",
	"FLAG_SERIES":        "Run tasks given on the CLI in series (the default is parallel).",
	"FLAG_LOG_LEVEL":     "Set the loglevel. -L for least verbose and -LLLL for most verbose. -LLL is default.",
}
