package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPaneWidths(t *testing.T) {
	if l, r := paneWidths(60); l != 60 || r != 0 {
		t.Fatalf("narrow %d %d", l, r)
	}
	l, r := paneWidths(120)
	if l != 48 || l+r+lipgloss.Width(paneSep) != 120 {
		t.Fatalf("wide %d %d", l, r)
	}
}

func TestJoinPanesExactSize(t *testing.T) {
	out := joinPanes([]string{"a", strings.Repeat("x", 50)}, []string{"right"}, 10, 8, 3)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	for _, l := range lines {
		if lipgloss.Width(l) != 10+3+8 {
			t.Fatalf("width %d: %q", lipgloss.Width(l), l)
		}
	}
	if !strings.Contains(lines[1], "…") {
		t.Fatal("long line not clipped")
	}
}

func TestKeepVisibleAndWindow(t *testing.T) {
	top := 0
	keepVisible(&top, 9, 20, 5)
	if top != 5 {
		t.Fatalf("top %d", top)
	}
	keepVisible(&top, 2, 20, 5)
	if top != 2 {
		t.Fatalf("top %d", top)
	}
	lines := []string{"1", "2", "3"}
	top = 7
	if got := window(lines, &top, 2); top != 1 || len(got) != 2 || got[0] != "2" {
		t.Fatalf("%d %v", top, got)
	}
	if n := numbered("a\n\tb\n"); len(n) != 2 || n[1] != "2      b" {
		t.Fatalf("%q", n)
	}
}
