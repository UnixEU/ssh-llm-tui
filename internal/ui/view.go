package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// View renders the current screen. The help bar is the last lines and the
// whole view is exactly the terminal height, so the bar is not scrolled away.
func (m *Model) View() string {
	if m.width < 1 || m.height < 1 {
		return ""
	}
	footer := m.helpBar()
	fh := lineCount(footer)
	if fh >= m.height {
		return m.exact(footer, m.height, true)
	}
	bodyH := m.height - fh
	m.layoutBody(bodyH)
	var body string
	switch m.screen {
	case screenWelcome:
		body = m.viewWelcome()
	case screenModels:
		body = m.viewModels()
	case screenLoading:
		body = m.viewLoading()
	case screenChat:
		body = m.viewChat(bodyH)
	case screenContext:
		body = m.viewContext()
	case screenTarget:
		body = m.viewTarget()
	case screenFit:
		body = m.viewFit()
	case screenChanges:
		body = m.viewChanges(bodyH)
	default:
		body = "ssh-llm-tui"
	}
	body = m.exact(body, bodyH, m.screen == screenChat)
	if footer == "" {
		return body
	}
	if body == "" {
		return footer
	}
	return body + "\n" + footer
}

func (m *Model) viewWelcome() string {
	if raw, ok := readBanner(m.cfg.Welcome.BannerFile); ok {
		if centered, fit := centerBanner(raw, m.width); fit {
			art := m.paintBanner(centered)
			if panel := m.gpuPanel(false); panel != "" {
				return joinLines(m.alignRightBlock(panel), art)
			}
			return art
		}
	}
	header := m.headerBlock(m.cfg.Welcome.Title, "v"+m.version)
	who := m.theme.subtitle.Render("signed in as " + m.user)
	body := ""
	if strings.TrimSpace(m.cfg.Welcome.Message) != "" {
		body = m.boxed(m.cfg.Welcome.Message)
	}
	if body == "" {
		body = m.theme.title.Render(m.cfg.Welcome.Title)
	}
	hint := m.theme.accent.Render(nonEmpty(m.cfg.Welcome.Hint, "Press any key to continue"))
	extra := ""
	if m.errText != "" {
		extra = m.theme.err.Render(m.errText)
	} else if m.status != "" {
		extra = m.theme.muted.Render(m.status)
	}
	return joinLines(header, who, "", body, "", hint, extra)
}

func (m *Model) viewModels() string {
	header := m.headerBlock(m.cfg.Models.Title, "v"+m.version)
	var main string
	switch {
	case m.fetching:
		main = m.theme.spinner.Render(m.spinner.View()+" ") + m.theme.status.Render(nonEmpty(m.cfg.Models.Loading, "Fetching models…"))
	case m.errText != "":
		main = m.boxed(strings.Join([]string{
			"Could not list models on Docker Model Runner.",
			"",
			"URL: " + m.cfg.DMR.BaseURL,
			m.errText,
			"",
			"Press r to retry.",
		}, "\n"))
	case len(m.models) == 0:
		main = m.boxed(m.cfg.Models.Empty)
	default:
		main = m.list.View()
	}
	hintText := m.cfg.Models.Hint
	if m.status != "" && !m.fetching {
		hintText = m.status
	}
	hint := ""
	if strings.TrimSpace(hintText) != "" {
		hint = m.theme.muted.Width(max(1, m.width)).Render(hintText)
	}
	return joinLines(header, hint, main)
}

func (m *Model) viewLoading() string {
	title := m.cfg.Loading.Title
	if m.loadKind == loadUnload {
		title = "Freeing the GPU"
	}
	if m.loadKind == loadProbe {
		title = "Checking memory"
	}
	header := m.headerBlock(title, "v"+m.version)
	line := m.theme.spinner.Render(m.spinner.View()+" ") + m.theme.text.Render(m.status)
	noteText := "Other SSH sessions share this GPU. A model change here unloads runners for everyone."
	if m.loadKind == loadProbe {
		noteText = "Reading the model size and the free GPU and system memory. Nothing is loaded yet."
	}
	note := m.theme.muted.Width(max(1, m.width)).Render(noteText)
	return joinLines(header, "", m.boxed(line), "", note)
}

func (m *Model) viewChat(bodyH int) string {
	right := (dmr.Model{ID: m.modelID}).DisplayName()
	if right != "" && m.ctxSize > 0 {
		right += " · ctx " + ctxLabel(m.ctxSize) + " · " + targetShort(m.target)
	}
	if right == "" {
		right = "v" + m.version
	}
	title := m.cfg.Chat.Title
	if m.editActive() {
		title += " · EDIT"
		if n := m.workspace().Pending(); n > 0 {
			title += fmt.Sprintf(" · %d to review", n)
		}
	}
	inputH := 1
	if !m.compact() && bodyH >= 10 {
		inputH = 3
	}
	status := m.statusText()
	statusH := 0
	if status != "" {
		statusH = 1
	}
	maxHeader := bodyH - inputH - statusH - 1
	if maxHeader < 1 {
		status = ""
		statusH = 0
		maxHeader = bodyH - inputH - 1
	}
	if maxHeader < 1 {
		maxHeader = 0
	}
	header := ""
	if maxHeader > 0 {
		header = m.fitHeader(title, right, maxHeader)
	}
	headerH := 0
	if header != "" {
		headerH = lineCount(header)
	}
	vpH := bodyH - headerH - statusH - inputH
	if vpH < 1 {
		status = ""
		statusH = 0
		vpH = bodyH - headerH - inputH
	}
	if vpH < 1 && inputH > 0 {
		inputH = bodyH - headerH
		if inputH < 0 {
			inputH = 0
		}
		vpH = bodyH - headerH - inputH
	}
	if vpH < 1 {
		header = ""
		headerH = 0
		vpH = bodyH - inputH
		if vpH < 1 {
			vpH = bodyH
			inputH = 0
		}
	}
	var parts []string
	box, boxH := m.chatFileBox(vpH - 1)
	vpH -= boxH
	if vpH < 1 {
		vpH = bodyH - headerH - statusH - inputH
		box = ""
		boxH = 0
		if vpH < 1 {
			vpH = 1
		}
	}
	if m.viewport.Width != m.width || m.viewport.Height != vpH {
		m.viewport.Width = m.width
		m.viewport.Height = vpH
		m.syncTranscript(m.viewport.AtBottom())
	}
	if headerH > 0 {
		parts = append(parts, header)
	}
	if vpH > 0 {
		parts = append(parts, m.viewport.View())
	}
	if statusH > 0 {
		parts = append(parts, status)
	}
	if box != "" {
		parts = append(parts, box)
	}
	if inputH > 0 {
		parts = append(parts, m.inputBlock(inputH))
	}
	return strings.Join(parts, "\n")
}

func (m *Model) statusText() string {
	if m.errText != "" {
		return m.clipWidth(m.theme.err.Render(m.errText))
	}
	if m.streaming && !m.thinkingNow() && strings.TrimSpace(m.partial) != "" {
		return m.clipWidth(m.theme.status.Render("Generating…"))
	}
	if m.streaming && strings.TrimSpace(m.partial) == "" && strings.TrimSpace(m.partialR) == "" && !m.think.InThink() {
		return m.clipWidth(m.theme.status.Render("Generating…"))
	}
	return ""
}

func (m *Model) inputBlock(h int) string {
	field := m.clipWidth(m.input.View())
	if h <= 1 || m.width < 8 {
		return field
	}
	inner := m.width - 4
	if inner < 1 {
		return field
	}
	boxed := m.theme.input.Width(inner).Render(m.input.View())
	return m.exact(boxed, h, false)
}

func (m *Model) helpBar() string {
	full := m.helpLabels(false)
	short := m.helpLabels(true)
	maxLines := 2
	if m.height <= 10 {
		maxLines = 1
	}
	lines := wrapParts(m.width, "  ·  ", full)
	if len(lines) > maxLines {
		lines = wrapParts(m.width, "  ·  ", short)
	}
	if len(lines) > maxLines {
		lines = wrapParts(m.width, " ", short)
	}
	if len(lines) > maxLines {
		lines = []string{fitOneLine(m.width, short)}
	}
	if len(lines) == 0 {
		lines = []string{"ctrl+q exit"}
	}
	painted := make([]string, 0, len(lines))
	for _, line := range lines {
		painted = append(painted, m.paintBar(line))
	}
	return strings.Join(painted, "\n")
}

func (m *Model) helpLabels(short bool) []string {
	switch m.screen {
	case screenWelcome:
		if short {
			return []string{"any key", "ctrl+q"}
		}
		return []string{"any key continue", "ctrl+q exit"}
	case screenModels:
		if short {
			return []string{"enter", "r", "ctrl+q"}
		}
		return []string{"enter select", "r refresh", "ctrl+q exit"}
	case screenLoading:
		if short {
			return []string{"ctrl+q"}
		}
		return []string{"ctrl+q exit"}
	case screenContext, screenTarget, screenFit:
		return m.fitHelp(short)
	case screenChanges:
		return m.changesHelp(short)
	default:
		if m.filesFocus {
			if short {
				return []string{"space", "d", "D clear", "j/k", "esc", "ctrl+q"}
			}
			return []string{"space include (file/dir)", "d remove", "D clear all", "j/k pgup g/G", "esc/ctrl+f back", "ctrl+q exit"}
		}
		if short {
			out := []string{"enter", "ctrl+t", "ctrl+r", "ctrl+n", "ctrl+o", "ctrl+f"}
			if m.editsReady() {
				out = append(out, "ctrl+e")
			}
			return append(out, "pgup", "esc", "ctrl+q")
		}
		md := "ctrl+r raw"
		if !m.markdown {
			md = "ctrl+r render"
		}
		opts := []string{"enter send", "ctrl+t thinking", md, "ctrl+n model"}
		if m.cfg.ConfiguresDMR() {
			opts = append(opts, "ctrl+o options")
		}
		opts = append(opts, "ctrl+f files")
		if m.editsReady() {
			opts = append(opts, "ctrl+e changes")
		}
		return append(opts, "pgup/pgdn scroll", "esc welcome", "ctrl+q exit")
	}
}

func (m *Model) headerBlock(title, right string) string {
	return m.composeHeader(title, right, false)
}

func (m *Model) fitHeader(title, right string, maxH int) string {
	if maxH < 1 {
		return ""
	}
	block := m.composeHeader(title, right, false)
	if lineCount(block) <= maxH {
		return block
	}
	block = m.composeHeader(title, right, true)
	if lineCount(block) <= maxH {
		return block
	}
	return m.exact(block, maxH, false)
}

func (m *Model) composeHeader(title, right string, compact bool) string {
	left := title
	if right != "" && !strings.HasPrefix(right, "v") {
		left = title + " · " + right
	}
	panel := m.gpuPanel(compact)
	if panel == "" {
		corner := right
		if !strings.HasPrefix(right, "v") {
			corner = "v" + m.version
		}
		return m.theme.titleLine(m.width, title, corner)
	}
	return m.placeBeside(left, panel)
}

func (m *Model) gpuPanel(forceCompact bool) string {
	if m.gpu == nil {
		return ""
	}
	snap := m.gpu.Snapshot()
	if snap.Disabled {
		return ""
	}
	if !snap.OK || len(snap.GPUs) == 0 {
		if snap.Message != "" {
			return snap.Message
		}
		return "GPU n/a"
	}
	r := m.theme.r
	return renderGPUPanel(r, snap.GPUs, m.width, m.gpuThresholds(), forceCompact)
}

func (m *Model) placeBeside(left, panel string) string {
	lines := strings.Split(panel, "\n")
	var out []string
	leftS := ""
	if strings.TrimSpace(left) != "" {
		leftS = m.theme.title.Render(left)
	}
	first := lines[0]
	if leftS != "" && lipgloss.Width(leftS)+1+lipgloss.Width(first) <= m.width {
		gap := m.width - lipgloss.Width(leftS) - lipgloss.Width(first)
		if gap < 1 {
			gap = 1
		}
		out = append(out, leftS+strings.Repeat(" ", gap)+first)
		lines = lines[1:]
	} else if leftS != "" {
		out = append(out, m.clipWidth(leftS))
	}
	for _, ln := range lines {
		out = append(out, m.alignRight(ln))
	}
	return strings.Join(out, "\n")
}

func (m *Model) alignRight(line string) string {
	if m.width < 1 {
		return ""
	}
	if lipgloss.Width(line) >= m.width {
		return m.clipWidth(line)
	}
	return strings.Repeat(" ", m.width-lipgloss.Width(line)) + line
}

func (m *Model) alignRightBlock(block string) string {
	var out []string
	for _, ln := range strings.Split(block, "\n") {
		out = append(out, m.alignRight(ln))
	}
	return strings.Join(out, "\n")
}

// chatFileBox is the full-width file dock. maxH is how many lines the chat
// body can spare. Unfocused is one summary line. Focused is a title plus
// FILES_BOX_ROWS file lines.
func (m *Model) chatFileBox(maxH int) (string, int) {
	if m.files == nil || maxH < 1 {
		return "", 0
	}
	box := m.renderFileBox(m.filesFocus)
	h := lineCount(box)
	if h > maxH && m.filesFocus {
		box = m.renderFileBox(false)
		h = lineCount(box)
	}
	if h > maxH || h < 1 {
		return "", 0
	}
	return box, h
}

func (m *Model) clampFileTop(n, rows int) {
	if rows < 1 {
		rows = 1
	}
	maxTop := n - rows
	if maxTop < 0 {
		maxTop = 0
	}
	if m.fileTop < 0 {
		m.fileTop = 0
	}
	if m.fileTop > maxTop {
		m.fileTop = maxTop
	}
}

func scrollGlyph(index, top, rows, total int) string {
	if total <= rows {
		return " "
	}
	last := top + rows - 1
	if index == top && top > 0 {
		return "↑"
	}
	if index == last && last < total-1 {
		return "↓"
	}
	// Thumb sits on the row closest to the window's place in the list.
	span := total - rows
	if span < 1 {
		span = 1
	}
	thumb := top + (top*rows)/span
	if thumb < top {
		thumb = top
	}
	if thumb > last {
		thumb = last
	}
	if index == thumb {
		return "█"
	}
	return "│"
}

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%d KiB", (n+512)/1024)
	}
	mb := float64(n) / (1024 * 1024)
	return fmt.Sprintf("%.1f MiB", mb)
}

func fitMiddle(width int, left, name, right string) string {
	if width < 1 {
		return ""
	}
	lr := []rune(left)
	rr := []rune(right)
	nr := []rune(name)
	room := width - len(lr) - len(rr)
	if room < 1 {
		all := append(append(lr, nr...), rr...)
		if len(all) > width {
			if width == 1 {
				return "…"
			}
			all = append(all[:width-1], '…')
		}
		return string(all)
	}
	if len(nr) > room {
		if room == 1 {
			nr = []rune("…")
		} else {
			nr = append(nr[:room-1], '…')
		}
	}
	pad := room - len(nr)
	return left + string(nr) + strings.Repeat(" ", pad) + right
}

func (m *Model) paintBar(line string) string {
	line = m.clipWidth(line)
	return m.theme.bar.Width(max(1, m.width)).Render(line)
}

func (m *Model) transcript() string {
	width := m.viewport.Width
	if width < 1 {
		width = m.width
	}
	if width < 1 {
		width = 1
	}
	you := nonEmpty(m.cfg.Chat.YouLabel, "you")
	var blocks []string
	for _, item := range m.turns {
		blocks = append(blocks, m.renderTurn(item, width, you, false))
	}
	if m.streaming || m.partial != "" || m.partialR != "" || m.think.InThink() {
		blocks = append(blocks, m.renderTurn(turn{
			role:          roleAssistant,
			content:       m.partial,
			reasoning:     m.partialR,
			reasoningOpen: m.liveOpen,
			thinkFor:      m.liveElapsed(),
		}, width, you, true))
	}
	if len(blocks) == 0 {
		return m.theme.muted.Width(width).Render("Ask something to start. The conversation stays in this SSH session.")
	}
	return strings.Join(blocks, "\n\n")
}

func (m *Model) renderTurn(item turn, width int, you string, live bool) string {
	switch item.role {
	case roleNote:
		return m.theme.note.Width(width).Render("· " + item.content)
	case roleUser:
		return m.theme.youLabel.Render(you) + "\n" + m.theme.user.Width(width).Render(item.content)
	default:
		name := (dmr.Model{ID: m.modelID}).DisplayName()
		if name == "" {
			name = "model"
		}
		lines := []string{m.theme.modelLabel.Render(name)}
		if ind := m.reasoningIndicator(item, width, live); ind != "" {
			lines = append(lines, ind)
		}
		body := ""
		if item.content != "" {
			// A live reply stays plain text so tokens don't flicker through a
			// half-parsed document. Glamour runs once the reply is finished.
			if live || !m.markdown {
				body = m.theme.assistant.Width(width).Render(item.content)
			} else {
				body = m.renderAssistantMarkdown(item.content, width)
			}
		}
		if live {
			body += m.theme.accent.Render("▌")
		}
		if body != "" {
			lines = append(lines, body)
		}
		return strings.Join(lines, "\n")
	}
}

func (m *Model) reasoningIndicator(item turn, width int, live bool) string {
	thinking := live && m.thinkingNow()
	has := strings.TrimSpace(item.reasoning) != "" || thinking
	if !has {
		return ""
	}
	elapsed := item.thinkFor
	if live {
		elapsed = m.liveElapsed()
	}
	var label string
	switch {
	case thinking && item.reasoningOpen:
		label = "▾ Thinking… " + formatElapsed(elapsed) + "  (ctrl+t to collapse)"
	case thinking:
		label = "▸ Thinking… " + formatElapsed(elapsed) + "  (ctrl+t to expand)"
	case item.reasoningOpen:
		label = "▾ Thought for " + formatElapsed(elapsed) + "  (ctrl+t to collapse)"
	default:
		label = "▸ Thought for " + formatElapsed(elapsed) + "  (ctrl+t to expand)"
	}
	if thinking {
		label = m.spinner.View() + " " + label
	}
	out := m.theme.note.Width(width).Render(label)
	if item.reasoningOpen && strings.TrimSpace(item.reasoning) != "" {
		out += "\n" + m.theme.note.Width(width).Render(strings.TrimSpace(item.reasoning))
	}
	return out
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return "<1s"
	}
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
}

func (m *Model) boxed(text string) string {
	if m.width < 44 || m.height < 14 {
		return m.theme.text.Width(max(1, m.width)).Render(text)
	}
	inner := m.width - 4
	if inner < 8 {
		inner = m.width
	}
	return m.theme.panel.Width(inner).Render(text)
}

func (m *Model) layoutBody(bodyH int) {
	if bodyH < 1 {
		bodyH = 1
	}
	compact := m.compact()
	listH := bodyH - 2
	if listH < 1 {
		listH = 1
	}
	m.list.SetSize(m.width, listH)
	m.list.SetShowStatusBar(!compact && listH > 6)
	m.list.SetShowPagination(!compact && listH > 6)
	inputW := m.width - 6
	if inputW < 1 {
		inputW = 1
	}
	m.input.Width = inputW
}

func (m *Model) layout() {
	// Kept so older call sites still resize widgets. View does the real fit.
	m.layoutBody(max(1, m.height-1))
}

func (m *Model) exact(s string, n int, keepBottom bool) string {
	if n < 1 {
		return ""
	}
	s = strings.TrimRight(s, "\n")
	var lines []string
	if s != "" {
		lines = strings.Split(s, "\n")
	}
	for i, line := range lines {
		lines[i] = m.clipWidth(line)
	}
	if len(lines) > n {
		if keepBottom {
			lines = lines[len(lines)-n:]
		} else {
			lines = lines[:n]
		}
	}
	for len(lines) < n {
		if keepBottom {
			lines = append([]string{""}, lines...)
		} else {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) clipWidth(s string) string {
	if m.width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= m.width {
		return s
	}
	return ansi.Truncate(s, m.width, "")
}

// fitOneLine keeps the last hint (exit) and as many earlier hints as fit.
func fitOneLine(width int, parts []string) string {
	if width < 1 {
		width = 1
	}
	if len(parts) == 0 {
		return ""
	}
	exit := parts[len(parts)-1]
	if lipgloss.Width(exit) > width {
		return ansi.Truncate(exit, width, "")
	}
	prefix := ""
	for _, part := range parts[:len(parts)-1] {
		next := part
		if prefix != "" {
			next = prefix + " " + part
		}
		candidate := next + " " + exit
		if lipgloss.Width(candidate) > width {
			break
		}
		prefix = next
	}
	if prefix == "" {
		return exit
	}
	return prefix + " " + exit
}

func wrapParts(width int, sep string, parts []string) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	cur := ""
	for _, part := range parts {
		if lipgloss.Width(part) > width {
			part = ansi.Truncate(part, width, "")
		}
		if cur == "" {
			cur = part
			continue
		}
		next := cur + sep + part
		if lipgloss.Width(next) <= width {
			cur = next
			continue
		}
		lines = append(lines, cur)
		cur = part
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func joinLines(parts ...string) string {
	return strings.Join(parts, "\n")
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
