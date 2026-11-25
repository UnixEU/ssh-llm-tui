package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Two-pane full-screen views (files, changes): a list on the left and a
// scrollable detail pane on the right. On a narrow terminal only the
// focused pane is drawn.

// paneSep sits between the panes.
const paneSep = " │ "

// paneNarrow is the width below which one pane is drawn at a time.
const paneNarrow = 72

// paneWidths splits width into the list and detail widths. right is 0
// when the terminal is too narrow for both.
func paneWidths(width int) (left, right int) {
	if width < paneNarrow {
		return width, 0
	}
	left = width * 2 / 5
	if left < 30 {
		left = 30
	}
	if left > 64 {
		left = 64
	}
	right = width - left - lipgloss.Width(paneSep)
	if right < 20 {
		return width, 0
	}
	return left, right
}

// padTo clips s to width cells and pads it with spaces.
func padTo(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// joinPanes draws rows lines of left and right side by side. A nil right
// with rightW 0 draws the left pane alone.
func joinPanes(left, right []string, leftW, rightW, rows int) string {
	out := make([]string, 0, rows)
	for i := 0; i < rows; i++ {
		l := ""
		if i < len(left) {
			l = left[i]
		}
		if rightW <= 0 {
			out = append(out, padTo(l, leftW))
			continue
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		out = append(out, padTo(l, leftW)+paneSep+padTo(r, rightW))
	}
	return strings.Join(out, "\n")
}

// keepVisible moves *top so index sel stays inside a window of rows.
func keepVisible(top *int, sel, n, rows int) {
	if rows < 1 {
		rows = 1
	}
	if sel >= 0 {
		if sel < *top {
			*top = sel
		}
		if sel >= *top+rows {
			*top = sel - rows + 1
		}
	}
	clampTop(top, n, rows)
}

// clampTop keeps *top within 0 and n-rows.
func clampTop(top *int, n, rows int) {
	maxTop := n - rows
	if maxTop < 0 {
		maxTop = 0
	}
	if *top > maxTop {
		*top = maxTop
	}
	if *top < 0 {
		*top = 0
	}
}

// window returns rows lines of lines from *top, after clamping it.
func window(lines []string, top *int, rows int) []string {
	clampTop(top, len(lines), rows)
	end := *top + rows
	if end > len(lines) {
		end = len(lines)
	}
	return lines[*top:end]
}

// numbered prefixes each line with its line number and expands tabs.
func numbered(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	w := len(strconv.Itoa(len(lines)))
	out := make([]string, len(lines))
	for i, l := range lines {
		n := strconv.Itoa(i + 1)
		out[i] = strings.Repeat(" ", w-len(n)) + n + "  " + strings.ReplaceAll(strings.TrimRight(l, "\r"), "\t", "    ")
	}
	return out
}
