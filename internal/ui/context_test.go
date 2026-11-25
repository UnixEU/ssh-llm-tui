package ui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+x":
		return tea.KeyMsg{Type: tea.KeyCtrlX}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestCtxGaugeUsesUsageThenEstimates(t *testing.T) {
	m := testModel(t)
	m.modelID = "ai/smollm2"
	m.ctxSize = 8192
	if g := m.ctxGauge(); !strings.HasPrefix(g, "ctx ~") || !strings.Contains(g, "/8k") {
		t.Fatalf("estimate gauge %q", g)
	}
	m.turns = []turn{{role: roleUser, content: "hi"}, {role: roleAssistant, content: "hello"}}
	m.absorbUsage(&dmr.Usage{PromptTokens: 4000, CompletionTokens: 96, TokensPerSecond: 30})
	m.absorbUsage(&dmr.Usage{PromptTokens: 4000, CompletionTokens: 100})
	m.commitUsage()
	if m.usage.TokensPerSecond != 30 {
		t.Fatalf("speed lost: %+v", m.usage)
	}
	if g := m.ctxGauge(); g != "ctx 4.1k/8k 50%" {
		t.Fatalf("exact gauge %q", g)
	}
	m.turns = append(m.turns, turn{role: roleUser, content: strings.Repeat("x", 8000)})
	if g := m.ctxGauge(); !strings.HasPrefix(g, "ctx ~6.1k/8k 74%") {
		t.Fatalf("grown gauge %q", g)
	}
	m.turns = append(m.turns, turn{role: roleUser, content: strings.Repeat("x", 4000)})
	if g := m.ctxGauge(); !strings.Contains(g, "⚠ ctrl+x") {
		t.Fatalf("warn gauge %q", g)
	}
	m.ctxSize = 0
	if g := m.ctxGauge(); !strings.HasSuffix(g, "/?") {
		t.Fatalf("unknown size %q", g)
	}
	m.onCtxSize(ctxSizeMsg{model: "ai/smollm2", size: 16384})
	if n, src := m.ctxLimit(); n != 16384 || src != "configured in DMR" {
		t.Fatalf("detected %d %q", n, src)
	}
}

func TestCtxPanelReset(t *testing.T) {
	m := testModel(t)
	m.modelID = "ai/smollm2"
	m.screen = screenChat
	m.turns = []turn{{role: roleUser, content: "a"}, {role: roleAssistant, content: "b"}}
	m.summary = "old"
	m.Update(keyMsg("ctrl+x"))
	if m.screen != screenCtxInfo {
		t.Fatalf("screen %v", m.screen)
	}
	if v := m.View(); !strings.Contains(v, "In use") || !strings.Contains(v, "r  reset") {
		t.Fatalf("panel:\n%s", v)
	}
	m.Update(keyMsg("r"))
	if !m.ctxConfirm || !strings.Contains(m.View(), "Clear the chat history? y / n") {
		t.Fatal("no confirm")
	}
	m.Update(keyMsg("n"))
	if m.ctxConfirm || len(m.turns) != 2 {
		t.Fatal("n should keep the history")
	}
	m.Update(keyMsg("r"))
	m.Update(keyMsg("y"))
	if m.screen != screenChat || len(m.turns) != 1 || m.turns[0].role != roleNote || m.summary != "" {
		t.Fatalf("after reset %v %+v %q", m.screen, m.turns, m.summary)
	}
}

func TestSplitForCompress(t *testing.T) {
	turns := []turn{
		{role: roleNote}, {role: roleUser}, {role: roleAssistant},
		{role: roleUser}, {role: roleAssistant}, {role: roleUser}, {role: roleAssistant},
	}
	if got := splitForCompress(turns, 2); got != 3 {
		t.Fatalf("keep 2 = %d", got)
	}
	if got := splitForCompress(turns, 0); got != len(turns) {
		t.Fatalf("keep 0 = %d", got)
	}
	if got := splitForCompress(turns, 5); got != 0 {
		t.Fatalf("keep 5 = %d", got)
	}
}

func TestCompressReplacesOlderTurns(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<think>hmm</think>- user likes Go\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	client, err := dmr.New(dmr.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.LoadOptions{Getenv: func(k string) string {
		if k == "COMPRESS_KEEP_TURNS" {
			return "1"
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Config: cfg, Client: client, Renderer: lipgloss.NewRenderer(io.Discard), User: "user", Session: context.Background()})
	m.width, m.height = 100, 30
	m.layout()
	m.modelID = "ai/smollm2"
	m.screen = screenCtxInfo
	m.turns = []turn{
		{role: roleUser, content: "I write Go"}, {role: roleAssistant, content: "Nice"},
		{role: roleUser, content: "latest question"}, {role: roleAssistant, content: "latest answer"},
	}
	_, cmd := m.Update(keyMsg("c"))
	if cmd == nil || !m.compressing || m.screen != screenChat {
		t.Fatal("compress did not start")
	}
	if !strings.Contains(m.View(), "Compressing the history") {
		t.Fatalf("no status:\n%s", m.View())
	}
	m.Update(cmd())
	if !strings.Contains(body, "I write Go") || strings.Contains(body, "latest question") {
		t.Fatalf("compress request %s", body)
	}
	if m.compressing || m.summary != "- user likes Go" || len(m.turns) != 3 || m.turns[1].content != "latest question" {
		t.Fatalf("after compress %q %+v", m.summary, m.turns)
	}
	msgs := m.apiMessages()
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "Summary of the earlier conversation:\n- user likes Go") {
		t.Fatalf("system %+v", msgs[0])
	}
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "I write Go") {
			t.Fatal("compressed turn still sent")
		}
	}
}

func TestContextErrorHint(t *testing.T) {
	m := testModel(t)
	m.streaming = true
	m.finishStream(&dmr.HTTPError{Op: "chat", Status: 400, Body: "exceeds the available context size"})
	if !strings.Contains(m.errText, "ctrl+x to compress") {
		t.Fatalf("%q", m.errText)
	}
}
