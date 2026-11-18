package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// readBanner loads the ASCII art fresh. Missing, unreadable, or empty files
// return false so the welcome text is used instead. Only trailing blank
// lines and CR are removed. Inner spaces stay put.
func readBanner(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return "", false
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// centerBanner pads the art as a block. It returns false when any line is
// wider than the terminal, so the caller can fall back instead of wrapping.
func centerBanner(art string, width int) (string, bool) {
	if width < 1 {
		return "", false
	}
	lines := strings.Split(art, "\n")
	maxw := 0
	for _, ln := range lines {
		if n := lipgloss.Width(ln); n > maxw {
			maxw = n
		}
	}
	if maxw > width {
		return "", false
	}
	pad := (width - maxw) / 2
	prefix := strings.Repeat(" ", pad)
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n"), true
}

func (m *Model) paintBanner(art string) string {
	colors := []lipgloss.Color{"117", "213", "222", "150", "177"}
	lines := strings.Split(art, "\n")
	for i, ln := range lines {
		lines[i] = m.theme.title.Foreground(colors[i%len(colors)]).Render(ln)
	}
	return strings.Join(lines, "\n")
}
