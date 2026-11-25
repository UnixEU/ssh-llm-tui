package ui

import (
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func filesScreenModel(t *testing.T, width int) *Model {
	t.Helper()
	files := uploads.New(t.TempDir(), 1<<20, 100)
	for name, body := range map[string]string{
		"proj/main.go":   "package main\n\nfunc main() {}\n",
		"proj/README.md": "# Proj\n",
		"notes.txt":      strings.Repeat("line\n", 60),
	} {
		if err := files.Put("user", name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	m := testModel(t)
	m.files = files
	m.user = "user"
	m.width, m.height = width, 20
	m.screen = screenChat
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.screen != screenFiles {
		t.Fatalf("screen %v", m.screen)
	}
	return m
}

func TestFilesScreenTwoPanes(t *testing.T) {
	m := filesScreenModel(t, 110)
	view := m.View()
	if lipgloss.Height(view) != m.height {
		t.Fatalf("height %d\n%s", lipgloss.Height(view), view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("line too wide %d: %q", lipgloss.Width(line), line)
		}
	}
	if !strings.Contains(view, "of 256.0 MiB") || !strings.Contains(view, "│ notes.txt") || !strings.Contains(view, "1  line") {
		t.Fatalf("view:\n%s", view)
	}
	// Preview scrolls on its own once focused.
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !m.fileRight {
		t.Fatal("tab should focus the preview")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.previewTop == 0 || m.fileSel != "notes.txt" {
		t.Fatalf("preview top %d sel %q", m.previewTop, m.fileSel)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.fileRight || m.screen != screenFiles {
		t.Fatal("esc in the preview goes back to the list")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	for _, f := range m.files.List("user") {
		if f.Included {
			t.Fatalf("%s still included", f.Name)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.fileSel != "proj/README.md" || !strings.Contains(m.View(), "1  # Proj") {
		t.Fatalf("sel %q\n%s", m.fileSel, m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenChat {
		t.Fatal("esc should return to the chat")
	}
}

func TestFilesScreenNarrowShowsOnePane(t *testing.T) {
	m := filesScreenModel(t, 60)
	view := m.View()
	if strings.Contains(view, paneSep) {
		t.Fatalf("narrow view has two panes:\n%s", view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if view = m.View(); !strings.Contains(view, "1  line") {
		t.Fatalf("preview:\n%s", view)
	}
}
