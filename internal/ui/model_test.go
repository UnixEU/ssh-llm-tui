package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func testModel(t *testing.T) *Model {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{
		Config:   cfg,
		Renderer: lipgloss.NewRenderer(io.Discard),
		User:     "user",
		Version:  "0.1.0",
		Session:  context.Background(),
	})
	m.width = 80
	m.height = 24
	m.layout()
	return m
}

func TestEmptyModelListIsFriendly(t *testing.T) {
	m := testModel(t)
	m.screen = screenModels
	m.fetching = false
	m.models = nil
	m.errText = ""
	view := m.View()
	if !strings.Contains(view, "No models are pulled") {
		t.Fatalf("view missing empty-state copy:\n%s", view)
	}
	if !strings.Contains(view, "docker model pull") {
		t.Fatalf("view should tell the operator how to pull a model:\n%s", view)
	}
	if strings.Contains(view, "Could not list models") {
		t.Fatalf("empty list was rendered as an error:\n%s", view)
	}
}

func TestModelListErrorIsDistinctFromEmpty(t *testing.T) {
	m := testModel(t)
	m.screen = screenModels
	m.errText = "connection refused"
	view := m.View()
	if !strings.Contains(view, "Could not list models") {
		t.Fatalf("view:\n%s", view)
	}
	if !strings.Contains(view, m.cfg.DMR.BaseURL) {
		t.Fatalf("error view should include the DMR URL:\n%s", view)
	}
}

func TestViewFitsTerminal(t *testing.T) {
	m := testModel(t)
	m.models = []dmr.Model{{ID: "ai/smollm2", Parameters: "360M"}}
	cmd := m.list.SetItems(modelItems(m.models))
	if cmd != nil {
		_ = cmd()
	}
	m.modelID = "ai/smollm2"
	m.turns = []turn{{role: roleUser, content: "hello"}, {role: roleAssistant, content: strings.Repeat("word ", 80)}}
	m.syncTranscript(true)

	screens := []screen{screenWelcome, screenModels, screenLoading, screenChat}
	for _, h := range []int{6, 10, 16, 24, 40} {
		for _, w := range []int{20, 40, 80, 120} {
			m.width = w
			m.height = h
			for _, sc := range screens {
				m.screen = sc
				m.status = "warming the model into GPU memory for a long status line that should stay on one row"
				view := m.View()
				if got := lipgloss.Height(view); got != h {
					t.Fatalf("screen %d at %dx%d rendered %d rows\n%s", sc, w, h, got, view)
				}
				if !strings.Contains(view, "ctrl+q") {
					t.Fatalf("screen %d at %dx%d help bar missing ctrl+q:\n%s", sc, w, h, view)
				}
				for _, line := range strings.Split(view, "\n") {
					if lipgloss.Width(line) > w {
						t.Fatalf("screen %d at %dx%d line wider than the terminal (%d): %q", sc, w, h, lipgloss.Width(line), line)
					}
				}
			}
		}
	}
}

func TestWelcomeKeyOpensModels(t *testing.T) {
	m := testModel(t)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(*Model)
	if got.screen != screenModels {
		t.Fatalf("screen %d", got.screen)
	}
	if cmd == nil {
		t.Fatal("expected a fetch command")
	}
}

func TestWelcomeEscDoesNothing(t *testing.T) {
	m := testModel(t)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(*Model)
	if got.screen != screenWelcome {
		t.Fatalf("screen %d", got.screen)
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("esc must not exit")
		}
	}
}

func TestChatEscReturnsToWelcome(t *testing.T) {
	m := testModel(t)
	m.screen = screenChat
	m.modelID = "ai/gpt-oss:20b"
	m.turns = []turn{{role: roleUser, content: "hi"}}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := next.(*Model)
	if got.screen != screenLoading {
		t.Fatalf("screen %d", got.screen)
	}
	if cmd == nil {
		t.Fatal("expected unload")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		t.Fatal("esc must not close the session")
	}
	next, _ = got.Update(msg)
	got = next.(*Model)
	if got.screen != screenWelcome {
		t.Fatalf("screen %d", got.screen)
	}
	if got.modelID != "" || len(got.turns) != 0 {
		t.Fatal("chat state should reset")
	}
}

func TestCtrlCCancelsWithoutExiting(t *testing.T) {
	m := testModel(t)
	m.screen = screenChat
	m.streaming = true
	m.partial = "partial"
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := next.(*Model)
	if got.screen != screenChat || got.streaming {
		t.Fatalf("screen %d streaming %v", got.screen, got.streaming)
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("ctrl+c must not exit")
		}
	}
	_, cmd = got.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd == nil {
		t.Fatal("ctrl+q should exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+q should quit")
	}
}

func TestFriendlyModelNameAndReasoningStayOutOfHistory(t *testing.T) {
	m := testModel(t)
	m.screen = screenChat
	m.modelID = "docker.io/ai/gpt-oss:20b"
	m.turns = []turn{
		{role: roleUser, content: "hi"},
		{role: roleAssistant, content: "hello", reasoning: "thinking out loud"},
	}
	m.syncTranscript(true)
	view := m.View()
	if !strings.Contains(view, "ai/gpt-oss:20b") {
		t.Fatalf("view should show the friendly name:\n%s", view)
	}
	if strings.Contains(view, "docker.io/") {
		t.Fatalf("view should not show the docker.io prefix:\n%s", view)
	}
	if strings.Contains(view, "thinking out loud") {
		t.Fatalf("reasoning should stay collapsed:\n%s", view)
	}
	if !strings.Contains(view, "Thought for") || !strings.Contains(view, "hello") {
		t.Fatalf("collapsed indicator and answer should be visible:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+t") {
		t.Fatalf("chat help should mention ctrl+t:\n%s", view)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	opened := next.(*Model).View()
	if !strings.Contains(opened, "thinking out loud") {
		t.Fatalf("ctrl+t should expand the latest reasoning:\n%s", opened)
	}
	msgs := m.apiMessages()
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "thinking") {
			t.Fatalf("reasoning was sent back to the API: %+v", msgs)
		}
	}
	if msgs[len(msgs)-1].Content != "hello" {
		t.Fatalf("assistant content = %q", msgs[len(msgs)-1].Content)
	}
}

func TestThinkTagsCollapseAndStayOutOfHistory(t *testing.T) {
	m := testModel(t)
	m.screen = screenChat
	m.modelID = "ai/qwen"
	m.streaming = true
	m.absorb("Hello <thi", "")
	m.absorb("nk>hidden</think> there", "")
	m.finishStream(nil)
	view := m.View()
	if strings.Contains(view, "hidden") || strings.Contains(view, "<think>") {
		t.Fatalf("think tag leaked into the view:\n%s", view)
	}
	if !strings.Contains(view, "Hello") || !strings.Contains(view, "there") {
		t.Fatalf("answer missing:\n%s", view)
	}
	if !strings.Contains(view, "Thought for") {
		t.Fatalf("expected a collapsed thought indicator:\n%s", view)
	}
	for _, msg := range m.apiMessages() {
		if strings.Contains(msg.Content, "hidden") || strings.Contains(msg.Content, "think>") {
			t.Fatalf("think text was sent back to the API: %+v", msg)
		}
	}
}

func TestReasoningTokensConcatenate(t *testing.T) {
	m := testModel(t)
	m.streaming = true
	m.absorb("", "Hel")
	m.absorb("", "lo")
	m.absorb("!", "")
	m.finishStream(nil)
	last := m.turns[len(m.turns)-1]
	if last.reasoning != "Hello" || last.content != "!" {
		t.Fatalf("reasoning %q content %q", last.reasoning, last.content)
	}
	if strings.Contains(m.View(), "Hello") {
		t.Fatal("concatenated reasoning should stay collapsed")
	}
}

func TestClearAllUploadsConfirms(t *testing.T) {
	files := uploads.New(t.TempDir(), 1000, 10)
	if err := files.Put("user", "a.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := files.Put("user", "b.txt", []byte("world")); err != nil {
		t.Fatal(err)
	}
	m := testModel(t)
	m.files = files
	m.user = "user"
	m.screen = screenChat
	m.width = 90
	m.height = 30

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	got := next.(*Model)
	view := got.View()
	if !strings.Contains(view, "D clear") && !strings.Contains(view, "clear all") {
		t.Fatalf("focused help missing clear:\n%s", view)
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	got = next.(*Model)
	if !got.fileConfirm || !strings.Contains(got.View(), "y/n") {
		t.Fatal("expected y/n confirm")
	}
	if len(files.List("user")) != 2 {
		t.Fatal("files deleted before confirm")
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got = next.(*Model)
	if got.fileConfirm || len(files.List("user")) != 2 {
		t.Fatal("n should cancel the clear")
	}
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	got = next.(*Model)
	next, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got = next.(*Model)
	if len(files.List("user")) != 0 {
		t.Fatalf("files remain: %+v", files.List("user"))
	}
	if _, err := os.Stat(filepath.Join(files.Dir(), "user")); !os.IsNotExist(err) {
		t.Fatalf("user dir not removed: %v", err)
	}
}

func TestCtrlQQuits(t *testing.T) {
	m := testModel(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd == nil {
		t.Fatal("expected quit")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("msg %#v", msg)
	}
}
