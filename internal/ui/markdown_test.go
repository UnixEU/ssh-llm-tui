package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestMarkdownRendersAtWidths(t *testing.T) {
	src := strings.Join([]string{
		"# Heading",
		"",
		"A **bold** word and an *italic* one, plus `inline`.",
		"",
		"- first",
		"- second",
		"",
		"```go",
		"fmt.Println(\"hi\")",
		"```",
		"",
		"| col | value |",
		"| --- | ----- |",
		"| a | " + strings.Repeat("wide ", 30) + "|",
		"",
		"See [docs](https://example.com/guide).",
		"",
		strings.Repeat("sentence ", 30),
	}, "\n")
	m := testModel(t)
	if !m.markdown {
		t.Fatal("markdown should default on")
	}
	var prev string
	for _, width := range []int{32, 80} {
		out := m.renderAssistantMarkdown(src, width)
		if strings.Contains(out, "**") || strings.Contains(out, "```") || strings.Contains(out, "| --- |") {
			t.Fatalf("width %d still has raw markdown:\n%s", width, out)
		}
		if !strings.Contains(out, "Heading") || !strings.Contains(out, "Println") || !strings.Contains(out, "example.com") {
			t.Fatalf("width %d dropped content:\n%s", width, out)
		}
		if strings.Contains(out, "\x1b]") {
			t.Fatalf("width %d emitted an OSC sequence", width)
		}
		for _, line := range strings.Split(out, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d line is %d: %q", width, lipgloss.Width(line), line)
			}
		}
		if prev != "" && prev == out {
			t.Fatal("narrow and wide renders were identical")
		}
		prev = out
		again := m.renderAssistantMarkdown(src, width)
		if again != out {
			t.Fatal("cached render changed")
		}
	}
}

func TestMarkdownToggleShowsRaw(t *testing.T) {
	m := testModel(t)
	m.screen = screenChat
	m.modelID = "ai/qwen"
	m.turns = []turn{{role: roleAssistant, content: "say **hello**"}}
	m.syncTranscript(true)
	view := m.View()
	if strings.Contains(view, "**hello**") {
		t.Fatalf("rendered view still shows markers:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+r") {
		t.Fatalf("help missing ctrl+r:\n%s", view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	got := next.(*Model)
	if got.markdown {
		t.Fatal("ctrl+r should turn rendering off")
	}
	raw := got.View()
	if !strings.Contains(raw, "**hello**") {
		t.Fatalf("raw view:\n%s", raw)
	}
	if lipgloss.Height(raw) != got.height {
		t.Fatalf("height %d", lipgloss.Height(raw))
	}
}
