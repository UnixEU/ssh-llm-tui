package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
)

// theme holds lipgloss styles bound to one SSH session's renderer so
// concurrent sessions do not share a global color profile.
type theme struct {
	r          *lipgloss.Renderer
	title      lipgloss.Style
	version    lipgloss.Style
	subtitle   lipgloss.Style
	text       lipgloss.Style
	muted      lipgloss.Style
	accent     lipgloss.Style
	user       lipgloss.Style
	assistant  lipgloss.Style
	note       lipgloss.Style
	err        lipgloss.Style
	ok         lipgloss.Style
	footer     lipgloss.Style
	key        lipgloss.Style
	panel      lipgloss.Style
	input      lipgloss.Style
	status     lipgloss.Style
	spinner    lipgloss.Style
	youLabel   lipgloss.Style
	modelLabel lipgloss.Style
	bar        lipgloss.Style
	files      lipgloss.Style
	fileNote   lipgloss.Style
}

func newTheme(r *lipgloss.Renderer) theme {
	if r == nil {
		r = lipgloss.NewRenderer(io.Discard)
	}
	pink := lipgloss.Color("213")
	violet := lipgloss.Color("99")
	cyan := lipgloss.Color("117")
	green := lipgloss.Color("150")
	red := lipgloss.Color("203")
	gold := lipgloss.Color("222")
	fg := lipgloss.AdaptiveColor{Light: "#1c1917", Dark: "#f4f4f5"}
	muted := lipgloss.AdaptiveColor{Light: "#57534e", Dark: "#a1a1aa"}

	return theme{
		r:         r,
		title:     r.NewStyle().Bold(true).Foreground(pink),
		version:   r.NewStyle().Foreground(muted),
		subtitle:  r.NewStyle().Foreground(cyan),
		text:      r.NewStyle().Foreground(fg),
		muted:     r.NewStyle().Foreground(muted),
		accent:    r.NewStyle().Foreground(gold).Bold(true),
		user:      r.NewStyle().Foreground(cyan),
		assistant: r.NewStyle().Foreground(fg),
		note:      r.NewStyle().Italic(true).Foreground(muted),
		err:       r.NewStyle().Foreground(red),
		ok:        r.NewStyle().Foreground(green),
		footer:    r.NewStyle().Foreground(muted),
		key:       r.NewStyle().Foreground(gold),
		panel: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(violet).
			Padding(1, 2),
		input: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(violet).
			Padding(0, 1),
		status:     r.NewStyle().Foreground(cyan),
		spinner:    r.NewStyle().Foreground(gold),
		youLabel:   r.NewStyle().Bold(true).Foreground(cyan),
		modelLabel: r.NewStyle().Bold(true).Foreground(pink),
		bar: r.NewStyle().
			Foreground(gold).
			Background(lipgloss.Color("236")),
		files:    r.NewStyle().Foreground(violet).Bold(true),
		fileNote: r.NewStyle().Foreground(lipgloss.Color("208")).Faint(true),
	}
}

func styleList(r *lipgloss.Renderer, l *list.Model) {
	if r == nil {
		r = lipgloss.NewRenderer(io.Discard)
	}
	pink := lipgloss.Color("213")
	fg := lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#f4f4f5"}
	muted := lipgloss.AdaptiveColor{Light: "#6b6375", Dark: "#a1a1aa"}

	l.Styles.Title = r.NewStyle().
		Background(lipgloss.Color("99")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1).
		Bold(true)

	d := list.NewDefaultDelegate()
	d.Styles.NormalTitle = r.NewStyle().Foreground(fg).Padding(0, 0, 0, 2)
	d.Styles.NormalDesc = d.Styles.NormalTitle.Foreground(muted)
	d.Styles.SelectedTitle = r.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(pink).
		Foreground(pink).
		Bold(true).
		Padding(0, 0, 0, 1)
	d.Styles.SelectedDesc = d.Styles.SelectedTitle.Foreground(muted).Bold(false)
	d.Styles.DimmedTitle = r.NewStyle().Foreground(muted).Padding(0, 0, 0, 2)
	d.Styles.DimmedDesc = d.Styles.DimmedTitle.Foreground(muted)
	d.Styles.FilterMatch = r.NewStyle().Underline(true).Foreground(pink)
	l.SetDelegate(d)
}

func (t theme) titleLine(width int, left, right string) string {
	leftS := t.title.Render(left)
	rightS := t.version.Render(right)
	gap := width - lipgloss.Width(leftS) - lipgloss.Width(rightS)
	if gap < 1 {
		if width < 1 {
			return leftS
		}
		return clipANSI(leftS, width)
	}
	return leftS + strings.Repeat(" ", gap) + rightS
}

func clipANSI(s string, width int) string {
	if width < 1 || lipgloss.Width(s) <= width {
		return s
	}
	// Fall back to a hard cut. Callers use this for short labels.
	plain := lipgloss.NewStyle().Render(s)
	_ = plain
	runes := []rune(s)
	if len(runes) > width {
		return string(runes[:width])
	}
	return s
}

func fitHeight(s string, height int, keepBottom bool) string {
	if height < 1 {
		return ""
	}
	if lipgloss.Height(s) <= height {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	if keepBottom {
		lines = lines[len(lines)-height:]
	} else {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func padBottom(s string, height int) string {
	if height < 1 {
		return ""
	}
	h := lipgloss.Height(s)
	if h > height {
		return fitHeight(s, height, false)
	}
	if h < height {
		s += strings.Repeat("\n", height-h)
	}
	return s
}

func fillModel(msg, model string) string {
	if strings.TrimSpace(model) == "" {
		model = "the model"
	}
	msg = strings.ReplaceAll(msg, "{model}", model)
	msg = strings.ReplaceAll(msg, "{{model}}", model)
	return msg
}

func modelItems(models []dmr.Model) []list.Item {
	items := make([]list.Item, 0, len(models))
	for _, model := range models {
		items = append(items, modelItem{model: model})
	}
	return items
}

type modelItem struct {
	model dmr.Model
}

func (m modelItem) Title() string       { return m.model.DisplayName() }
func (m modelItem) Description() string { return m.model.Summary() }
func (m modelItem) FilterValue() string {
	return m.model.DisplayName() + " " + m.model.ID + " " + m.model.Summary()
}

func oneLine(err error) string {
	if err == nil {
		return ""
	}
	s := strings.Join(strings.Fields(err.Error()), " ")
	if len(s) > 280 {
		s = s[:280] + "…"
	}
	return s
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
