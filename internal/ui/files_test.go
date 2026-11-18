package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestFileBoxCollapsedAndSingleLine(t *testing.T) {
	files := uploads.New(t.TempDir(), 5<<20, 10)
	files.SetImageLimits(64, 256<<10)
	png := mustUIImage(t)
	if err := files.Put("user", strings.Repeat("x", 80)+".png", png); err != nil {
		t.Fatal(err)
	}
	m := testModel(t)
	m.files = files
	m.user = "user"
	m.screen = screenChat
	m.width = 60
	m.height = 24
	view := m.View()
	if lipgloss.Height(view) != m.height {
		t.Fatalf("view height %d want %d", lipgloss.Height(view), m.height)
	}
	if !strings.Contains(view, "files: 1 (1 incl, ~0/8.2kt, 1 img) — ctrl+f") {
		t.Fatalf("summary missing:\n%s", view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := next.(*Model)
	focused := got.View()
	if lipgloss.Height(focused) != got.height {
		t.Fatalf("focused height %d want %d\n%s", lipgloss.Height(focused), got.height, focused)
	}
	box := got.renderFileBox(true)
	for _, line := range strings.Split(box, "\n") {
		if strings.Contains(line, "\n") {
			t.Fatal("wrapped")
		}
		if lipgloss.Width(line) > got.width {
			t.Fatalf("line width %d: %q", lipgloss.Width(line), line)
		}
	}
	joined := box
	if !strings.Contains(joined, "img ") || !strings.Contains(joined, "…") {
		t.Fatalf("row %s", joined)
	}
	if strings.Contains(joined, "~0t") {
		t.Fatalf("image row used a token estimate: %s", joined)
	}
	if !strings.Contains(joined, "scp -P 2222") {
		t.Fatalf("scp hint missing: %s", joined)
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	left := next.(*Model)
	if left.filesFocus || left.screen != screenChat {
		t.Fatalf("esc should leave the file box, focus %v screen %v", left.filesFocus, left.screen)
	}
}

func TestImagesAttachOnceAndNonVisionSkips(t *testing.T) {
	files := uploads.New(t.TempDir(), 5<<20, 10)
	if err := files.Put("user", "shot.png", mustUIImage(t)); err != nil {
		t.Fatal(err)
	}
	m := testModel(t)
	m.files = files
	m.user = "user"
	m.modelID = "docker.io/ai/qwen3.8:27b-q4_K_M"
	m.turns = []turn{{role: roleUser, content: "what is in the picture?"}}
	msgs := m.apiMessages()
	last := msgs[len(msgs)-1]
	if last.Role != "user" || len(last.Parts) != 2 || last.Parts[1].ImageURL == nil {
		t.Fatalf("parts %+v", last)
	}
	body, err := last.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "data:image/") {
		t.Fatalf("json %s", body)
	}
	var named bool
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "shot.png") {
			named = true
		}
	}
	if !named {
		t.Fatalf("context did not name the file: %+v", msgs)
	}
	m.files.MarkSent("user", []string{"shot.png"})
	m.pendingImages = nil
	m.turns = append(m.turns,
		turn{role: roleAssistant, content: "a picture"},
		turn{role: roleUser, content: "and now?"},
	)
	again := m.apiMessages()
	if len(again[len(again)-1].Parts) != 0 {
		t.Fatalf("image was resent: %+v", again[len(again)-1].Parts)
	}

	m.cfg.Uploads.NonVisionModels = "ai/gpt-oss*"
	m.modelID = "docker.io/ai/gpt-oss:20b"
	if err := files.SetIncluded("user", "shot.png", true); err != nil {
		t.Fatal(err)
	}
	m.pendingImages = nil
	m.turns = []turn{{role: roleUser, content: "see this"}}
	skipped := m.apiMessages()
	if len(skipped[len(skipped)-1].Parts) != 0 {
		t.Fatal("non-vision model still got image parts")
	}
	if m.imagesAllowed() {
		t.Fatal("gpt-oss should be blocked by NON_VISION_MODELS")
	}
	m.cfg.Uploads.VisionModels = "*qwen3.8*"
	m.cfg.Uploads.NonVisionModels = ""
	m.modelID = "docker.io/ai/qwen3.8:27b-q4_K_M"
	m.noVision = map[string]bool{m.modelID: true}
	if !m.imagesAllowed() {
		t.Fatal("VISION_MODELS should override the session cache")
	}
	errHTTP := &dmr.HTTPError{Status: 400, Body: "vision not supported, no mmproj"}
	m.cfg.Uploads.VisionModels = ""
	m.pendingImages = []string{"shot.png"}
	m.noVision = nil
	if !m.retryWithoutImages(errHTTP) {
		t.Fatal("expected retry")
	}
	if m.errText != visionNotice || m.imagesAllowed() {
		t.Fatalf("notice %q allowed %v", m.errText, m.imagesAllowed())
	}
}

func TestFileSummaryScroll(t *testing.T) {
	files := uploads.New(t.TempDir(), 1000, 10)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"} {
		if err := files.Put("user", name, []byte("hello "+name)); err != nil {
			t.Fatal(err)
		}
	}
	m := testModel(t)
	m.files = files
	m.cfg.Uploads.FilesBoxRows = 3
	m.filesFocus = true
	m.ensureFileSel()
	box := m.renderFileBox(true)
	lines := strings.Split(box, "\n")
	if len(lines) != 4 {
		t.Fatalf("title+3 rows, got %d\n%s", len(lines), box)
	}
	if !strings.Contains(lines[len(lines)-1], "↓") && !strings.Contains(box, "█") && !strings.Contains(box, "│") {
		t.Fatalf("missing scroll marker\n%s", box)
	}
	m.pageFiles(1)
	if m.fileTop == 0 {
		t.Fatal("pgdn should scroll")
	}
}

func TestFocusedFileBoxShowsNote(t *testing.T) {
	files := uploads.New(t.TempDir(), 1<<20, 10)
	broken := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte{0, 1, 2, 3}...)
	if err := files.Put("user", "photo.png", broken); err != nil {
		t.Fatal(err)
	}
	if err := files.Put("user", "data.bin", []byte{0, 1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	m := testModel(t)
	m.files = files
	m.user = "user"
	m.screen = screenChat
	m.width = 80
	m.height = 24
	collapsed := m.View()
	if !strings.Contains(collapsed, "files: 2 (2 incl, ~0/8.2kt, 0 img) — ctrl+f") {
		t.Fatalf("summary:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "image:") || strings.Contains(collapsed, "not UTF-8") {
		t.Fatalf("unfocused box should stay one summary line:\n%s", collapsed)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := next.(*Model)
	view := got.View()
	if lipgloss.Height(view) != got.height {
		t.Fatalf("height %d want %d\n%s", lipgloss.Height(view), got.height, view)
	}
	box := got.renderFileBox(true)
	if !strings.Contains(box, "! not UTF-8 text") {
		t.Fatalf("selected binary note missing:\n%s", box)
	}
	for _, line := range strings.Split(box, "\n") {
		if lipgloss.Width(line) > got.width {
			t.Fatalf("line width %d: %q", lipgloss.Width(line), line)
		}
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	got = next.(*Model)
	box = got.renderFileBox(true)
	if !strings.Contains(box, "! image:") {
		t.Fatalf("selected image note missing:\n%s", box)
	}
}

func mustUIImage(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 20, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
