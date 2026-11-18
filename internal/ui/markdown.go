package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

type mdEntry struct {
	width int
	out   string
}

// renderAssistantMarkdown renders finished assistant text with a fixed dark
// style. The color profile is set explicitly so glamour does not query the
// terminal background (that OSC request breaks over SSH).
func (m *Model) renderAssistantMarkdown(src string, width int) string {
	if width < 1 {
		width = 1
	}
	src = strings.TrimRight(src, "\n")
	if src == "" {
		return ""
	}
	if m.mdCache == nil {
		m.mdCache = map[string]mdEntry{}
	}
	if hit, ok := m.mdCache[src]; ok && hit.width == width {
		return hit.out
	}
	out := m.markdownPlain(src, width)
	if rendered, err := m.glamourRender(src, width); err == nil && strings.TrimSpace(rendered) != "" {
		out = clipMarkdown(rendered, width)
	}
	m.mdCache[src] = mdEntry{width: width, out: out}
	return out
}

func (m *Model) glamourRender(src string, width int) (string, error) {
	if m.mdByWidth == nil {
		m.mdByWidth = map[int]*glamour.TermRenderer{}
	}
	r := m.mdByWidth[width]
	if r == nil {
		var err error
		wrap := width
		if wrap < 8 {
			wrap = 8
		}
		r, err = glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(wrap),
			glamour.WithColorProfile(termenv.ANSI256),
			glamour.WithTableWrap(true),
		)
		if err != nil {
			return "", err
		}
		m.mdByWidth[width] = r
	}
	out, err := r.Render(src)
	if err != nil {
		return "", err
	}
	return strings.Trim(out, "\n"), nil
}

func (m *Model) markdownPlain(src string, width int) string {
	return m.theme.assistant.Width(width).Render(src)
}

func clipMarkdown(s string, width int) string {
	if width < 1 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}
