package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func treeStore(t *testing.T) *uploads.Store {
	t.Helper()
	files := uploads.New(t.TempDir(), 1<<20, 2000)
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("repo/pkg%02d/sub/file%04d.go", i%25, i)
		if err := files.Put("user", name, []byte("package p // "+name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.Put("user", "repo/README.md", []byte("readme")); err != nil {
		t.Fatal(err)
	}
	if err := files.Put("user", "solo.txt", []byte("solo")); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestFileTreeRowsAndDirToggle(t *testing.T) {
	files := treeStore(t)
	m := testModel(t)
	m.files = files
	m.user = "user"
	m.screen = screenChat
	m.width = 90
	m.height = 30
	m.cfg.Uploads.FilesBoxRows = 8

	items := m.fileItems()
	// 1002 files + repo/ + 25 pkg dirs + 25 sub dirs.
	if len(items) != 1002+1+25+25 {
		t.Fatalf("rows %d", len(items))
	}
	if items[0].key != "repo/" || items[1].key != "repo/README.md" || items[2].key != "repo/pkg00/" || items[3].key != "repo/pkg00/sub/" {
		t.Fatalf("order %v %v %v %v", items[0].key, items[1].key, items[2].key, items[3].key)
	}
	if items[len(items)-1].key != "solo.txt" {
		t.Fatal(items[len(items)-1].key)
	}
	if !items[0].dir || items[0].count != 1001 {
		t.Fatalf("repo totals %+v", items[0])
	}
	// Auto-include stopped at the default number of files.
	if items[0].incl != uploads.DefaultAutoInclude {
		t.Fatalf("auto-included %d", items[0].incl)
	}

	start := time.Now()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m = next.(*Model)
	if m.fileSel != "repo/" {
		t.Fatalf("sel %q", m.fileSel)
	}
	// space on a directory includes everything under it.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = next.(*Model)
	for _, f := range files.List("user") {
		if strings.HasPrefix(f.Name, "repo/") && !f.Included {
			t.Fatalf("%s not included", f.Name)
		}
	}
	box := m.renderFileBox(true)
	if !strings.Contains(box, "over") {
		t.Fatalf("token gauge should show over budget:\n%s", box)
	}
	// And again excludes it all.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = next.(*Model)
	for _, f := range files.List("user") {
		if strings.HasPrefix(f.Name, "repo/") && f.Included {
			t.Fatalf("%s still included", f.Name)
		}
	}

	// Scroll to the end and back; every frame fits the terminal.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = next.(*Model)
	if m.fileSel != "solo.txt" {
		t.Fatalf("G sel %q", m.fileSel)
	}
	view := m.View()
	if lipgloss.Height(view) != m.height || !strings.Contains(view, "solo.txt") {
		t.Fatalf("end view:\n%s", view)
	}
	for i := 0; i < 200; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		m = next.(*Model)
	}
	if m.fileTop != 0 {
		t.Fatalf("top %d", m.fileTop)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = next.(*Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(*Model)
	if m.fileSel != "repo/README.md" {
		t.Fatalf("j sel %q", m.fileSel)
	}
	for _, line := range strings.Split(m.renderFileBox(true), "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("line width %d: %q", lipgloss.Width(line), line)
		}
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("file box too slow with 1000 files: %v", time.Since(start))
	}

	// d on a directory removes the subtree.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(*Model)
	if m.fileSel != "repo/pkg00/" {
		t.Fatalf("sel %q", m.fileSel)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = next.(*Model)
	for _, f := range files.List("user") {
		if strings.HasPrefix(f.Name, "repo/pkg00/") {
			t.Fatal("subtree survived")
		}
	}
	if m.fileSel != "repo/pkg01/" {
		t.Fatalf("selection after remove %q", m.fileSel)
	}
}

func TestTreeLess(t *testing.T) {
	if !treeLess("a", "a/x") || !treeLess("a/x", "a-b") || treeLess("b", "a/z") {
		t.Fatal("order")
	}
}
