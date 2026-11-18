package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBannerStripsOnlyTrailingBlankLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "banner.txt")
	raw := "  _ \r\n / \\\r\n\r\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := readBanner(path)
	if !ok {
		t.Fatal("expected banner")
	}
	if got != "  _ \n / \\" {
		t.Fatalf("got %q", got)
	}
	if _, ok := readBanner(filepath.Join(dir, "missing.txt")); ok {
		t.Fatal("missing file should fall back")
	}
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readBanner(empty); ok {
		t.Fatal("empty file should fall back")
	}
}

func TestCenterBannerRefusesNarrowTerminals(t *testing.T) {
	art := "12345\n  x"
	if _, ok := centerBanner(art, 4); ok {
		t.Fatal("narrow terminal should not wrap the art")
	}
	got, ok := centerBanner(art, 9)
	if !ok {
		t.Fatal("expected fit")
	}
	lines := strings.Split(got, "\n")
	if lines[0] != "  12345" || lines[1] != "    x" {
		t.Fatalf("%q", lines)
	}
}

func TestWelcomeUsesBannerAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "banner.txt")
	if err := os.WriteFile(path, []byte("BANNER-ART-LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := testModel(t)
	m.width = 40
	m.height = 16
	m.cfg.Welcome.BannerFile = path
	m.cfg.Welcome.Title = "ssh-llm-tui"
	m.cfg.Welcome.Message = "under the art"
	view := m.View()
	if !strings.Contains(view, "BANNER-ART-LINE") {
		t.Fatalf("banner missing:\n%s", view)
	}
	if strings.Contains(view, "under the art") || strings.Contains(view, "ssh-llm-tui") {
		t.Fatalf("banner view should hide title and message:\n%s", view)
	}
	m.width = 8
	m.height = 16
	narrow := m.View()
	if strings.Contains(narrow, "BANNER-ART-LINE") {
		t.Fatalf("narrow view should fall back, got:\n%s", narrow)
	}
	if !strings.Contains(narrow, "under") && !strings.Contains(narrow, "ssh-llm") {
		t.Fatalf("fallback should show title or message:\n%s", narrow)
	}
}
