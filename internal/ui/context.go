package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/think"
	tea "github.com/charmbracelet/bubbletea"
)

// Context gauge and the ctrl+x panel: how much of the model's context the
// conversation uses, a reset, and a compress that replaces older turns
// with a summary written by the model.

// ctxSizeMsg carries the context size DMR reports for the model, used
// when the session did not choose one.
type ctxSizeMsg struct {
	gen   int
	model string
	size  int
}

// compressMsg is the finished summary.
type compressMsg struct {
	gen     int
	summary string
	cut     int
	err     error
}

// fetchCtxSize asks DMR for the configured context of model.
func (m *Model) fetchCtxSize(model string) tea.Cmd {
	if m.client == nil || model == "" {
		return nil
	}
	client, gen, parent := m.client, m.gen, m.session
	if parent == nil {
		parent = context.Background()
	}
	return func() tea.Msg {
		cfgs, err := client.Configs(parent, model)
		if err != nil {
			return ctxSizeMsg{gen: gen, model: model}
		}
		for _, c := range cfgs {
			if c.ContextSize > 0 && dmr.SameModel(c.Model, model) {
				return ctxSizeMsg{gen: gen, model: model, size: c.ContextSize}
			}
		}
		return ctxSizeMsg{gen: gen, model: model}
	}
}

func (m *Model) onCtxSize(msg ctxSizeMsg) (tea.Model, tea.Cmd) {
	if msg.model == m.modelID && m.ctxSize == 0 {
		m.ctxDetected = msg.size
	}
	return m, nil
}

// ctxLimit is the context size and where it comes from.
func (m *Model) ctxLimit() (int, string) {
	if m.ctxSize > 0 {
		return m.ctxSize, "chosen for this session"
	}
	if m.ctxDetected > 0 {
		return m.ctxDetected, "configured in DMR"
	}
	for _, mod := range m.models {
		if mod.ID == m.modelID && mod.ContextWindow > 0 {
			return mod.ContextWindow, "model maximum, DMR may load less"
		}
	}
	return 0, "unknown"
}

// ctxParts estimates the prompt in tokens (bytes / 4) by part.
func (m *Model) ctxParts() (system, files, summary, chat int) {
	system = len(m.systemPrompt()) / 4
	if m.files != nil {
		files = includedTokens(m.files.List(m.user))
		if m.budget > 0 && files > m.budget {
			files = m.budget
		}
	}
	summary = len(m.summary) / 4
	for _, t := range m.turns {
		if t.role != roleNote {
			chat += len(t.content) / 4
		}
	}
	return system, files, summary, chat
}

// ctxUsed is the context in use. exact is true when it is the backend's
// count from the last reply and nothing was added since.
func (m *Model) ctxUsed() (int, bool) {
	if m.usage != nil && m.usageTurns == len(m.turns) {
		return m.usage.Total(), true
	}
	if m.usage != nil && m.usageTurns < len(m.turns) {
		extra := 0
		for _, t := range m.turns[m.usageTurns:] {
			if t.role != roleNote {
				extra += len(t.content) / 4
			}
		}
		return m.usage.Total() + extra, false
	}
	s, f, sum, c := m.ctxParts()
	return s + f + sum + c, false
}

func ctxPercent(used, limit int) int {
	if limit <= 0 {
		return 0
	}
	return int(float64(used)/float64(limit)*100 + 0.5)
}

// ctxGauge is the header text, "ctx 12.4k/32k 39%".
func (m *Model) ctxGauge() string {
	limit, _ := m.ctxLimit()
	used, exact := m.ctxUsed()
	u := shortCount(used)
	if !exact {
		u = "~" + u
	}
	if limit <= 0 {
		return "ctx " + u + "/?"
	}
	pct := ctxPercent(used, limit)
	s := fmt.Sprintf("ctx %s/%s %d%%", u, ctxLabel(limit), pct)
	if pct >= m.cfg.Chat.WarnPct {
		s += " ⚠ ctrl+x"
	}
	return s
}

// absorbUsage keeps the latest counts. Speed from an earlier chunk stays
// when a later one has none.
func (m *Model) absorbUsage(u *dmr.Usage) {
	if u == nil {
		return
	}
	next := *u
	if next.TokensPerSecond == 0 && m.pendingUsage != nil {
		next.TokensPerSecond = m.pendingUsage.TokensPerSecond
	}
	m.pendingUsage = &next
}

// commitUsage runs when a reply is finished and in m.turns.
func (m *Model) commitUsage() {
	if m.pendingUsage != nil {
		m.usage = m.pendingUsage
		m.usageTurns = len(m.turns)
	}
	m.pendingUsage = nil
}

func (m *Model) openCtxPanel() (tea.Model, tea.Cmd) {
	if m.modelID == "" {
		return m, nil
	}
	m.ctxConfirm = false
	m.ctxNote = ""
	m.screen = screenCtxInfo
	return m, nil
}

func (m *Model) onCtxPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.ctxConfirm {
		switch key {
		case "y", "Y":
			m.resetHistory()
			m.ctxConfirm = false
			m.screen = screenChat
			m.syncTranscript(true)
		case "n", "N", "esc":
			m.ctxConfirm = false
		}
		return m, nil
	}
	switch key {
	case "esc", "ctrl+x", "q":
		m.screen = screenChat
		m.syncTranscript(true)
		return m, nil
	case "r":
		if m.streaming {
			m.ctxNote = "Wait for the reply to finish, or press ctrl+c in the chat."
			return m, nil
		}
		m.ctxConfirm = true
	case "c":
		return m.startCompress()
	}
	return m, nil
}

// resetHistory clears the conversation and the summary. Files stay.
func (m *Model) resetHistory() {
	m.turns = []turn{{role: roleNote, content: "History cleared. Uploaded files stay in the prompt."}}
	m.summary = ""
	m.usage = nil
	m.pendingUsage = nil
	m.usageTurns = 0
	m.errText = ""
}

// splitForCompress returns the index of the first turn kept verbatim: the
// last keep user messages and what follows them stay.
func splitForCompress(turns []turn, keep int) int {
	if keep <= 0 {
		return len(turns)
	}
	seen := 0
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role == roleUser {
			seen++
			if seen == keep {
				return i
			}
		}
	}
	return 0
}

// compressTranscript is the text the model summarises.
func compressTranscript(prev string, turns []turn) string {
	var b strings.Builder
	if strings.TrimSpace(prev) != "" {
		b.WriteString("Earlier summary:\n")
		b.WriteString(strings.TrimSpace(prev))
		b.WriteString("\n\n")
	}
	b.WriteString("Conversation:\n")
	for _, t := range turns {
		switch t.role {
		case roleUser:
			b.WriteString("User: ")
		case roleAssistant:
			b.WriteString("Assistant: ")
		default:
			continue
		}
		b.WriteString(strings.TrimSpace(t.content))
		b.WriteString("\n\n")
	}
	return b.String()
}

func (m *Model) startCompress() (tea.Model, tea.Cmd) {
	if m.streaming {
		m.ctxNote = "Wait for the reply to finish, or press ctrl+c in the chat."
		return m, nil
	}
	cut := splitForCompress(m.turns, m.cfg.Chat.KeepTurns)
	n := 0
	for _, t := range m.turns[:cut] {
		if t.role != roleNote {
			n++
		}
	}
	if n == 0 {
		m.ctxNote = fmt.Sprintf("Nothing to compress: only the last %s are in the history.", plural(m.cfg.Chat.KeepTurns, "exchange", "exchanges"))
		return m, nil
	}
	if m.client == nil {
		m.ctxNote = "DMR client is not configured."
		return m, nil
	}
	ctx, gen := m.begin()
	m.streaming = true
	m.compressing = true
	m.status = "Compressing the history…"
	m.errText = ""
	m.screen = screenChat
	m.syncTranscript(true)
	msgs := []dmr.Message{
		{Role: "system", Content: m.cfg.Chat.CompressPrompt},
		{Role: "user", Content: compressTranscript(m.summary, m.turns[:cut])},
	}
	ch := m.client.ChatStream(ctx, m.modelID, msgs)
	return m, collectSummary(gen, cut, ch)
}

// collectSummary reads the whole stream and keeps only visible text.
func collectSummary(gen, cut int, ch <-chan dmr.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		var p think.Parser
		var out strings.Builder
		for ev := range ch {
			if ev.Err != nil {
				return compressMsg{gen: gen, cut: cut, err: ev.Err}
			}
			visible, _ := p.Push(ev.Content)
			out.WriteString(visible)
			if ev.Done {
				break
			}
		}
		c, _ := p.Flush()
		out.WriteString(c)
		return compressMsg{gen: gen, cut: cut, summary: strings.TrimSpace(out.String())}
	}
}

func (m *Model) onCompress(msg compressMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.streaming = false
	m.compressing = false
	m.status = ""
	switch {
	case msg.err != nil:
		m.errText = "Compress failed: " + oneLine(msg.err)
	case msg.summary == "":
		m.errText = "Compress failed: the model returned an empty summary."
	case msg.cut > len(m.turns):
		m.errText = "Compress failed: the history changed."
	default:
		n := 0
		for _, t := range m.turns[:msg.cut] {
			if t.role != roleNote {
				n++
			}
		}
		kept := append([]turn(nil), m.turns[msg.cut:]...)
		m.summary = msg.summary
		note := turn{role: roleNote, content: fmt.Sprintf("Compressed %s into a summary of ~%st. It is sent with the system prompt:\n%s",
			plural(n, "earlier message", "earlier messages"), shortCount(len(msg.summary)/4), msg.summary)}
		m.turns = append([]turn{note}, kept...)
		m.usage = nil
		m.pendingUsage = nil
		m.usageTurns = 0
	}
	m.syncTranscript(true)
	return m, nil
}

func (m *Model) viewCtxPanel() string {
	header := m.headerBlock("Context", "v"+m.version)
	limit, src := m.ctxLimit()
	used, exact := m.ctxUsed()
	pct := ctxPercent(used, limit)
	var lines []string
	add := func(label, value string) {
		lines = append(lines, fmt.Sprintf("%-11s %s", label, value))
	}
	add("Model", (dmr.Model{ID: m.modelID}).DisplayName())
	if limit > 0 {
		add("Size", fmt.Sprintf("%s tokens (%s)", ctxLabel(limit), src))
	} else {
		add("Size", "unknown. ctrl+o in the chat sets one")
	}
	usedText := shortCount(used) + " tokens"
	if !exact {
		usedText = "~" + usedText + " (estimate)"
	}
	if limit > 0 {
		usedText += fmt.Sprintf(" · %d%%  %s", pct, ctxBar(pct, 20))
	}
	add("In use", usedText)
	if u := m.usage; u != nil {
		last := fmt.Sprintf("prompt %s", shortCount(u.PromptTokens))
		if u.CachedTokens > 0 {
			last += fmt.Sprintf(" (cached %s)", shortCount(u.CachedTokens))
		}
		last += " · reply " + shortCount(u.CompletionTokens)
		if u.TokensPerSecond > 0 {
			last += fmt.Sprintf(" · %.1f tok/s", u.TokensPerSecond)
		}
		add("Last reply", last)
	}
	sys, files, sum, chat := m.ctxParts()
	parts := fmt.Sprintf("system ~%s · files ~%s · chat ~%s", shortCount(sys), shortCount(files), shortCount(chat))
	if sum > 0 {
		parts += " · summary ~" + shortCount(sum)
	}
	add("Parts", parts)
	msgs := 0
	for _, t := range m.turns {
		if t.role != roleNote {
			msgs++
		}
	}
	add("History", plural(msgs, "message", "messages"))
	lines = append(lines, "",
		"r  reset: clear the chat history. Uploaded files stay.",
		fmt.Sprintf("c  compress: the model summarises older messages, the last %s stay as they are.", plural(m.cfg.Chat.KeepTurns, "exchange", "exchanges")),
		"   Files are sent with every message; exclude some with ctrl+f to free more.",
	)
	body := m.boxed(strings.Join(lines, "\n"))
	extra := ""
	switch {
	case m.ctxConfirm:
		extra = m.theme.err.Render("Clear the chat history? y / n")
	case m.ctxNote != "":
		extra = m.theme.status.Render(m.ctxNote)
	}
	return joinLines(header, "", body, "", extra)
}

func ctxBar(pct, width int) string {
	if width < 1 {
		return ""
	}
	fill := pct * width / 100
	if fill > width {
		fill = width
	}
	if fill < 0 {
		fill = 0
	}
	return strings.Repeat("█", fill) + strings.Repeat("░", width-fill)
}

func (m *Model) ctxHelp(short bool) []string {
	if m.ctxConfirm {
		return []string{"y clear", "n keep", "ctrl+q exit"}
	}
	if short {
		return []string{"r", "c", "esc", "ctrl+q"}
	}
	return []string{"r reset", "c compress", "esc/ctrl+x chat", "ctrl+q exit"}
}
