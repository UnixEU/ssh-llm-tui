package ui

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/edits"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// sseServer answers chat completions with the given replies in turn,
// streamed in small chunks, and records the requests.
type sseServer struct {
	mu       sync.Mutex
	replies  []string
	requests []map[string]any
}

func (s *sseServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/engines/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.requests = append(s.requests, body)
	reply := ""
	if len(s.replies) > 0 {
		reply = s.replies[0]
		s.replies = s.replies[1:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(field, text string) {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{field: text}}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	// Reasoning that looks like a block must be ignored.
	chunk("reasoning_content", "FILE: proj/main.go\n<<<<<<< WHOLE\nhijack\n>>>>>>> WHOLE\n")
	for len(reply) > 0 {
		n := 7
		if n > len(reply) {
			n = len(reply)
		}
		chunk("content", reply[:n])
		reply = reply[n:]
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (s *sseServer) systemPrompt(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs, _ := s.requests[i]["messages"].([]any)
	var out []string
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		c, _ := mm["content"].(string)
		out = append(out, fmt.Sprint(mm["role"])+": "+c)
	}
	return strings.Join(out, "\n---\n")
}

// pump runs commands and feeds their messages back until none are left.
func pump(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for cmd != nil {
		if time.Now().After(deadline) {
			t.Fatal("pump timeout")
		}
		msg := cmd()
		if msg == nil {
			return
		}
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				pump(t, m, c)
			}
			return
		}
		_, cmd = m.Update(msg)
	}
}

func key(m *Model, s string) tea.Cmd {
	var msg tea.KeyMsg
	switch s {
	case "ctrl+e":
		msg = tea.KeyMsg{Type: tea.KeyCtrlE}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	_, cmd := m.Update(msg)
	return cmd
}

func editModel(t *testing.T, srv *httptest.Server) (*Model, *uploads.Store, *downloads.Store, *edits.Manager) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{Getenv: func(k string) string {
		if k == "PUBLIC_HOST" {
			return "llm.example.lan"
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	client, err := dmr.New(dmr.Options{BaseURL: srv.URL, RequestTimeout: 5 * time.Second, WarmTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	up := uploads.New(t.TempDir(), 1<<20, 100)
	dl := downloads.New(t.TempDir(), 1<<20)
	em := edits.NewManager()
	m := New(Options{Config: cfg, Client: client, Renderer: lipgloss.NewRenderer(io.Discard), User: "user", Session: context.Background(), Files: up, Downloads: dl, Edits: em})
	m.width, m.height = 120, 40
	m.screen = screenChat
	m.modelID = "ai/test"
	return m, up, dl, em
}

func send(t *testing.T, m *Model, text string) {
	t.Helper()
	m.input.SetValue(text)
	pump(t, m, key(m, "enter"))
}

func TestEditFlowAcceptExportArchive(t *testing.T) {
	stub := &sseServer{replies: []string{
		"Renamed the greeting.\n\nFILE: proj/main.go\n```go\n<<<<<<< SEARCH\n\tprintln(\"hi\")\n=======\n\tprintln(\"hello\")\n>>>>>>> REPLACE\n```\n\nFILE: proj/NOTES.md\n<<<<<<< WHOLE\n# Notes\n>>>>>>> WHOLE\n\nFILE: proj/README.md\n<<<<<<< SEARCH\nmissing line\n=======\nx\n>>>>>>> REPLACE\n",
		"FILE: proj/README.md\n<<<<<<< SEARCH\n# Demo\n=======\n# Demo app\n>>>>>>> REPLACE\n",
	}}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	m, up, dl, em := editModel(t, srv)
	up.Put("user", "proj/main.go", []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"))
	up.Put("user", "proj/README.md", []byte("# Demo\n"))

	send(t, m, "change the greeting")
	sys := stub.systemPrompt(0)
	if !strings.Contains(sys, "<<<<<<< SEARCH") || !strings.Contains(sys, "Deleting files is not allowed") || !strings.Contains(sys, "----- proj/main.go") {
		t.Fatalf("prompt:\n%s", sys)
	}
	last := m.turns[len(m.turns)-1]
	if last.role != roleNote || !strings.Contains(last.content, "Proposed changes to 2 files (1 new)") || !strings.Contains(last.content, "1 block could not be applied") {
		t.Fatalf("note %+v", last)
	}
	ws := em.For("user")
	if _, _, ok := ws.Version("proj/main.go"); ok {
		t.Fatal("applied without review")
	}
	if got, _ := up.Read("user", "proj/main.go"); !strings.Contains(string(got), "\"hi\"") {
		t.Fatal("upload changed")
	}
	if !strings.Contains(m.View(), "EDIT · 2 to review") {
		t.Fatalf("badge missing:\n%s", m.View())
	}

	key(m, "ctrl+e")
	if m.screen != screenChanges {
		t.Fatal("ctrl+e")
	}
	view := m.View()
	for _, want := range []string{"proj/main.go", "proj/NOTES.md", "pending", "NEW", "failed", "scp -P 2222 -r user@llm.example.lan:downloads/files .", "git apply changes.patch", "get -r downloads", "x export", "t whole folder"} {
		if !strings.Contains(view, want) {
			t.Fatalf("review screen missing %q:\n%s", want, view)
		}
	}
	m.chPath = "proj/main.go"
	key(m, "enter")
	if v := m.View(); !strings.Contains(v, "+\tprintln(\"hello\")") && !strings.Contains(v, "+    println(\"hello\")") {
		t.Fatalf("diff:\n%s", v)
	}
	key(m, "esc")
	key(m, "A")
	if !strings.Contains(m.chNote, "Accepted 2 changes") {
		t.Fatalf("note %q", m.chNote)
	}
	// f sends the failure back; the second reply fixes README.
	pump(t, m, key(m, "f"))
	if m.screen != screenChat {
		t.Fatal("f should return to chat")
	}
	retry := stub.systemPrompt(1)
	if !strings.Contains(retry, "could not be applied") || !strings.Contains(retry, "FILE: proj/README.md, SEARCH block 1") {
		t.Fatalf("retry:\n%s", retry)
	}
	// The retry prompt carries the accepted versions.
	if !strings.Contains(retry, "println(\"hello\")") || !strings.Contains(retry, "proj/NOTES.md (new in this session)") {
		t.Fatalf("accepted versions missing:\n%s", retry)
	}
	if !strings.Contains(m.turns[len(m.turns)-1].content, "Proposed changes to 1 file") {
		t.Fatalf("second note %+v", m.turns[len(m.turns)-1])
	}
	key(m, "ctrl+e")
	m.chPath = "proj/README.md"
	key(m, "a")
	pump(t, m, key(m, "x"))
	if !strings.Contains(m.chNote, "Exported 3 files") || !strings.Contains(m.chNote, "inside proj/") {
		t.Fatalf("export note %q", m.chNote)
	}
	if got, _ := dl.Read("user", "files/proj/main.go"); !strings.Contains(string(got), "hello") {
		t.Fatalf("exported %q", got)
	}
	patch, _ := dl.Read("user", "changes.patch")
	if !strings.Contains(string(patch), "--- a/main.go") || !strings.Contains(string(patch), "+++ b/NOTES.md") {
		t.Fatalf("patch:\n%s", patch)
	}
	pump(t, m, key(m, "t"))
	if !strings.Contains(m.chNote, "proj.tar.gz") {
		t.Fatalf("archive note %q", m.chNote)
	}
	f, _, err := dl.Open("user", "proj.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	if !strings.Contains(files["proj/main.go"], "hello") || files["proj/README.md"] != "# Demo app\n" || files["proj/NOTES.md"] != "# Notes\n" {
		t.Fatalf("archive %v", files)
	}
	if !strings.Contains(m.View(), "user@llm.example.lan:downloads/proj.tar.gz") {
		t.Fatalf("archive hint:\n%s", m.View())
	}
	// Undo and revert on the review screen.
	m.chPath = "proj/README.md"
	key(m, "u")
	if _, _, ok := ws.Version("proj/README.md"); ok {
		t.Fatal("undo")
	}
	m.chPath = "proj/main.go"
	key(m, "v")
	if _, _, ok := ws.Version("proj/main.go"); ok {
		t.Fatal("revert")
	}
	// m turns edit mode off: no instructions in the prompt.
	key(m, "m")
	if strings.Contains(m.systemPrompt(), "SEARCH") {
		t.Fatal("edit mode still on")
	}
	key(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc")
	}
}

func TestEditModeNeedsUploads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&sseServer{}).handler))
	defer srv.Close()
	m, up, _, _ := editModel(t, srv)
	if m.editActive() || strings.Contains(m.systemPrompt(), "SEARCH") {
		t.Fatal("edit mode without uploads")
	}
	up.Put("user", "a.txt", []byte("a"))
	if !m.editActive() {
		t.Fatal("edit mode should start with uploads")
	}
	m.cfg.Edit.Mode = "off"
	if m.editActive() {
		t.Fatal("EDIT_MODE=off")
	}
}

func TestChangesViewFitsTerminal(t *testing.T) {
	stub := &sseServer{replies: []string{"FILE: proj/a.go\n<<<<<<< SEARCH\nold\n=======\nnew line that is rather long " + strings.Repeat("x", 120) + "\n>>>>>>> REPLACE\n\nFILE: proj/b.md\n<<<<<<< WHOLE\nb\n>>>>>>> WHOLE\n"}}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	m, up, _, _ := editModel(t, srv)
	up.Put("user", "proj/a.go", []byte("old\n"))
	send(t, m, "go")
	key(m, "ctrl+e")
	for _, diff := range []bool{false, true} {
		if diff {
			m.chPath = "proj/a.go"
			key(m, "enter")
		}
		for _, h := range []int{6, 10, 16, 24, 40} {
			for _, w := range []int{20, 40, 80, 120} {
				m.width, m.height = w, h
				view := m.View()
				if got := lipgloss.Height(view); got != h {
					t.Fatalf("diff=%v %dx%d rendered %d rows\n%s", diff, w, h, got, view)
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > w {
						t.Fatalf("diff=%v %dx%d line too wide: %q", diff, w, h, line)
					}
				}
			}
		}
	}
}
