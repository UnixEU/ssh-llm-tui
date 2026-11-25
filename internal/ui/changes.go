package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/edits"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
)

// failureMessageMax caps the follow-up sent with f.
const failureMessageMax = 6000

// exportMsg reports an export or archive run in the background.
type exportMsg struct {
	note    string
	archive string
	err     error
}

// editsReady reports whether this session can propose edits at all.
func (m *Model) editsReady() bool {
	return m.cfg.EditOn() && m.edits != nil && m.files != nil
}

// editActive reports whether the next prompt carries edit instructions:
// edit mode is on and there are uploads or edited files.
func (m *Model) editActive() bool {
	if !m.editsReady() || !m.editOn {
		return false
	}
	if n, _ := m.files.Usage(m.user); n > 0 {
		return true
	}
	return len(m.workspace().Entries()) > 0
}

func (m *Model) workspace() *edits.Workspace { return m.edits.For(m.user) }

func (m *Model) editRules() edits.Rules {
	return edits.Rules{
		AllowNew:    m.cfg.EditAllowNew(),
		AllowDelete: m.cfg.EditAllowDelete(),
		MaxFiles:    m.cfg.Edit.MaxFiles,
		MaxBytes:    m.cfg.Uploads.MaxBytes,
		Seen:        m.seen,
	}
}

// systemPrompt is SYSTEM_PROMPT plus the edit format while edit mode is on.
func (m *Model) systemPrompt() string {
	base := strings.TrimSpace(m.cfg.Chat.SystemPrompt)
	if !m.editActive() {
		return base
	}
	instr := m.protocol.Instructions(m.editRules())
	if base == "" {
		return instr
	}
	return base + "\n\n" + instr
}

// proposeEdits reads the blocks of the last reply into the workspace.
func (m *Model) proposeEdits() {
	if !m.editActive() || len(m.turns) == 0 {
		return
	}
	last := m.turns[len(m.turns)-1]
	if last.role != roleAssistant || strings.TrimSpace(last.content) == "" {
		return
	}
	blocks, perrs := m.protocol.Extract(edits.Reply{Content: last.content})
	if len(blocks) == 0 && len(perrs) == 0 {
		return
	}
	res := m.workspace().Propose(m.files.View(m.user), blocks, perrs, m.editRules())
	m.turns = append(m.turns, turn{role: roleNote, content: proposalNote(res)})
}

func proposalNote(res edits.Result) string {
	var parts []string
	if n := res.Files(); n > 0 {
		s := "Proposed changes to " + plural(n, "file", "files")
		if k := res.NewFiles(); k > 0 {
			s += fmt.Sprintf(" (%d new)", k)
		}
		parts = append(parts, s)
	}
	if n := len(res.Failures); n > 0 {
		parts = append(parts, plural(n, "block", "blocks")+" could not be applied (f on the review screen sends them back)")
	}
	if len(parts) == 0 {
		return "No usable edit blocks in this reply."
	}
	return strings.Join(parts, "; ") + " · ctrl+e to review"
}

// openChanges shows the review screen.
func (m *Model) openChanges() (tea.Model, tea.Cmd) {
	if !m.editsReady() {
		return m, nil
	}
	m.screen = screenChanges
	m.chFocus = false
	m.chTop = 0
	m.ensureChangeSel()
	return m, nil
}

func (m *Model) changeEntries() []edits.Entry {
	if !m.editsReady() {
		return nil
	}
	return m.workspace().Entries()
}

func (m *Model) changeIndex(entries []edits.Entry) int {
	for i, e := range entries {
		if e.Path == m.chPath {
			return i
		}
	}
	return -1
}

func (m *Model) ensureChangeSel() {
	entries := m.changeEntries()
	if len(entries) == 0 {
		m.chPath = ""
		return
	}
	if m.changeIndex(entries) < 0 {
		// Prefer the first file waiting for review.
		m.chPath = entries[0].Path
		for _, e := range entries {
			if e.Change.Status == edits.StatusPending {
				m.chPath = e.Path
				break
			}
		}
	}
}

func (m *Model) moveChange(delta int) {
	entries := m.changeEntries()
	if len(entries) == 0 {
		return
	}
	i := m.changeIndex(entries)
	if i < 0 {
		i = 0
	}
	i += delta
	if i < 0 {
		i = 0
	}
	if i >= len(entries) {
		i = len(entries) - 1
	}
	if entries[i].Path != m.chPath {
		m.chPath = entries[i].Path
		m.chTop = 0
	}
}

func (m *Model) onChangesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ws := m.workspace()
	key := msg.String()
	rows := m.paneRows
	if rows < 3 {
		rows = 3
	}
	switch key {
	case "esc":
		if m.chFocus {
			m.chFocus = false
			return m, nil
		}
		m.screen = screenChat
		m.syncTranscript(true)
		return m, nil
	case "ctrl+e", "q":
		m.chFocus = false
		m.screen = screenChat
		m.syncTranscript(true)
		return m, nil
	case "tab", "enter":
		if m.chPath != "" {
			m.chFocus = !m.chFocus
		}
		return m, nil
	case "right", "l":
		if m.chPath != "" {
			m.chFocus = true
		}
		return m, nil
	case "left", "h":
		m.chFocus = false
		return m, nil
	case "d", "ctrl+r":
		m.chFileView = !m.chFileView
		m.chTop = 0
		if m.chFileView {
			m.chNote = "File view: the newest version with line numbers. d shows the diff."
		} else {
			m.chNote = "Diff view. d shows the whole file."
		}
		return m, nil
	case "b":
		m.chAgainstCurrent = !m.chAgainstCurrent
		m.chTop = 0
		if m.chAgainstCurrent {
			m.chNote = "Diff against the accepted version: only what the pending proposal changes."
		} else {
			m.chNote = "Diff against the uploaded original."
		}
		return m, nil
	case "]", "[":
		m.jumpHunk(key == "]")
		return m, nil
	case "up", "k":
		if m.chFocus {
			m.chTop--
		} else {
			m.moveChange(-1)
		}
	case "down", "j":
		if m.chFocus {
			m.chTop++
		} else {
			m.moveChange(1)
		}
	case "pgup", "ctrl+u":
		m.chTop -= scrollStep(rows)
	case "pgdown", "ctrl+d", " ":
		m.chTop += scrollStep(rows)
	case "home", "g":
		if m.chFocus {
			m.chTop = 0
		} else {
			m.moveChange(-1 << 30)
		}
	case "end", "G":
		if m.chFocus {
			m.chTop = 1 << 30
		} else {
			m.moveChange(1 << 30)
		}
	case "n":
		m.moveChange(1)
	case "p":
		m.moveChange(-1)
	default:
		return m.onChangesAction(ws, key)
	}
	if m.chTop < 0 {
		m.chTop = 0
	}
	return m, nil
}

// onChangesAction handles the keys shared by the list and the diff view.
func (m *Model) onChangesAction(ws *edits.Workspace, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "a":
		m.chNote = actionNote(ws.Accept(m.chPath), "Accepted "+m.chPath+".")
	case "r":
		m.chNote = actionNote(ws.Reject(m.chPath), "Rejected "+m.chPath+".")
	case "A":
		n, err := ws.AcceptAll()
		m.chNote = "Accepted " + plural(n, "change", "changes") + "."
		if err != nil {
			m.chNote += " " + oneLine(err)
		}
	case "R":
		m.chNote = "Rejected " + plural(ws.RejectAll(), "change", "changes") + "."
	case "u":
		m.chNote = actionNote(ws.Undo(m.chPath), "Undid the last change to "+m.chPath+".")
	case "v":
		m.chNote = actionNote(ws.Revert(m.chPath), "Reverted "+m.chPath+" to the upload. u brings the edit back.")
	case "m":
		m.editOn = !m.editOn
		if m.editOn {
			m.chNote = "Edit mode on: the model is told how to propose changes."
		} else {
			m.chNote = "Edit mode off: prompts no longer ask for edit blocks."
		}
	case "f":
		return m.sendFailures(ws)
	case "x":
		return m.startExport(false)
	case "t":
		return m.startExport(true)
	}
	m.ensureChangeSel()
	return m, nil
}

func actionNote(err error, ok string) string {
	switch {
	case err == nil:
		return ok
	case errors.Is(err, edits.ErrNoChange):
		return "Nothing to do for this file."
	}
	return oneLine(err)
}

// sendFailures asks the model to fix the blocks that did not apply.
func (m *Model) sendFailures(ws *edits.Workspace) (tea.Model, tea.Cmd) {
	fails := ws.Failures()
	if len(fails) == 0 {
		m.chNote = "No failed blocks in the latest reply."
		return m, nil
	}
	if m.streaming || m.modelID == "" {
		m.chNote = "Wait until the model has finished answering."
		return m, nil
	}
	ws.ClearFailures()
	m.screen = screenChat
	m.input.SetValue(edits.FailureMessage(fails, failureMessageMax))
	return m.submit()
}

// startExport writes downloads in the background: changed files and the
// patch, or (whole) the uploaded folder with accepted changes as tar.gz.
func (m *Model) startExport(whole bool) (tea.Model, tea.Cmd) {
	if m.downloads == nil {
		m.chNote = "Downloads are not configured."
		return m, nil
	}
	if m.exporting {
		return m, nil
	}
	ws := m.workspace()
	changed := ws.Changed()
	if !whole && len(changed) == 0 {
		m.chNote = "Nothing accepted yet. Accept a change with a, then press x."
		return m, nil
	}
	m.exporting = true
	m.chNote = "Writing downloads…"
	user, files, dl := m.user, m.files, m.downloads
	return m, func() tea.Msg {
		if whole {
			name, size, err := writeFolderArchive(user, files, ws, dl)
			if err != nil {
				return exportMsg{err: err}
			}
			return exportMsg{archive: name, note: fmt.Sprintf("Wrote downloads/%s (%s): the uploaded files with the accepted changes.", name, humanSize(size))}
		}
		n, root, err := exportChanges(user, files, changed, dl)
		if err != nil {
			return exportMsg{err: err}
		}
		note := fmt.Sprintf("Exported %s to downloads/files/ and the patch to downloads/%s.", plural(n, "file", "files"), downloads.PatchName)
		if root != "" {
			note += " The patch applies inside " + root + "/."
		}
		return exportMsg{note: note}
	}
}

func (m *Model) onExport(msg exportMsg) (tea.Model, tea.Cmd) {
	m.exporting = false
	if msg.err != nil {
		m.chNote = "Export failed: " + oneLine(msg.err)
		return m, nil
	}
	m.chNote = msg.note
	m.exported = true
	if msg.archive != "" {
		m.archive = msg.archive
	}
	return m, nil
}

// uploadedNames is every uploaded file plus files created by the model.
func uploadedNames(files *uploads.Store, user string, ws *edits.Workspace) []string {
	var names []string
	for _, f := range files.List(user) {
		names = append(names, f.Name)
	}
	return append(names, ws.Added()...)
}

func exportChanges(user string, files *uploads.Store, changed []edits.FileDiff, dl *downloads.Store) (int, string, error) {
	var paths []string
	for _, f := range files.List(user) {
		paths = append(paths, f.Name)
	}
	for _, c := range changed {
		paths = append(paths, c.Path)
	}
	root := edits.RootDir(paths)
	strip := ""
	if root != "" {
		strip = root + "/"
	}
	var out []downloads.File
	for _, c := range changed {
		if c.NewExists {
			out = append(out, downloads.File{Path: c.Path, Data: []byte(c.New)})
		}
	}
	if err := dl.Export(user, out, edits.Patch(changed, strip)); err != nil {
		return 0, "", err
	}
	return len(out), root, nil
}

// writeFolderArchive packs every uploaded file, with accepted changes
// applied and deleted files left out, plus new files.
func writeFolderArchive(user string, files *uploads.Store, ws *edits.Workspace, dl *downloads.Store) (string, int64, error) {
	names := uploadedNames(files, user, ws)
	if len(names) == 0 {
		return "", 0, errors.New("nothing uploaded")
	}
	root := edits.RootDir(names)
	prefix := ""
	name := "uploads" + downloads.ArchiveExt
	if root != "" {
		name = root + downloads.ArchiveExt
	} else {
		prefix = "uploads/"
	}
	var entries []downloads.ArchiveEntry
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		if text, exists, ok := ws.Version(n); ok {
			if exists {
				entries = append(entries, downloads.ArchiveEntry{Name: prefix + n, Data: []byte(text)})
			}
			continue
		}
		data, err := files.Read(user, n)
		if err != nil {
			continue
		}
		entries = append(entries, downloads.ArchiveEntry{Name: prefix + n, Data: data})
	}
	size, err := dl.WriteArchive(user, name, entries)
	return name, size, err
}

// sshTarget is "-P port user@host" for the download commands.
func (m *Model) sshTarget() (portFlag, sftpFlag, target string) {
	host := m.cfg.Downloads.PublicHost
	if host == "" {
		host = "host"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	user := m.user
	if strings.ContainsAny(user, " @:'\"\\") || user == "" {
		user = "user"
	}
	port := m.cfg.PublicPort()
	if port > 0 && port != 22 {
		portFlag = "-P " + strconv.Itoa(port) + " "
		sftpFlag = portFlag
	}
	return portFlag, sftpFlag, user + "@" + host
}

// downloadCommands are the copy-paste lines shown on the review screen.
func (m *Model) downloadCommands() []string {
	p, sp, target := m.sshTarget()
	cmds := []string{
		"scp " + p + "-r " + target + ":downloads/files .",
		"scp " + p + target + ":downloads/" + downloads.PatchName + " . && git apply " + downloads.PatchName,
	}
	if m.archive != "" {
		cmds = append(cmds, "scp "+p+target+":downloads/"+m.archive+" .")
	} else {
		cmds = append(cmds, "scp "+p+target+":downloads/<name>"+downloads.ArchiveExt+" .   (after t)")
	}
	cmds = append(cmds, "sftp "+sp+target+"   then: get -r downloads")
	return cmds
}

// diffRows is how many diff lines fit on the review screen.
func statusTag(e edits.Entry) string {
	switch {
	case e.Change.ID == 0 && e.Modified:
		return "accepted"
	case e.Change.ID == 0:
		return ""
	}
	return e.Change.Status.String()
}

func (m *Model) viewChanges(bodyH int) string {
	right := "edit mode off"
	if m.editOn {
		right = "edit mode on"
	}
	header := m.fitHeader("Changes", right, 2)
	entries := m.changeEntries()
	pending, accepted := 0, 0
	for _, e := range entries {
		if e.Change.Status == edits.StatusPending {
			pending++
		}
		if e.Modified {
			accepted++
		}
	}
	summary := fmt.Sprintf("%d waiting · %d changed against the upload", pending, accepted)
	if n := len(m.workspace().Failures()); n > 0 {
		summary += fmt.Sprintf(" · %s failed (f sends them back)", plural(n, "block", "blocks"))
	}
	mode := "diff"
	if m.chFileView {
		mode = "file"
	}
	summary += " · view: " + mode + " (d)"
	summaryLine := m.clipWidth(m.theme.muted.Render(summary))
	note := ""
	if m.chNote != "" {
		note = m.clipWidth(m.theme.status.Render(m.chNote))
	}
	dl := m.downloadBlock()
	rows := bodyH - lineCount(header) - 2
	if rows-lineCount(dl) >= 8 {
		rows -= lineCount(dl)
	} else {
		dl = ""
	}
	if rows < 1 {
		rows = 1
	}
	m.paneRows = rows
	leftW, rightW := paneWidths(m.width)
	sel := m.changeIndex(entries)
	var left []string
	if len(entries) == 0 {
		left = []string{
			m.theme.muted.Render("No changes yet."),
			"",
			m.theme.muted.Render("With edit mode on (m), ask the model to"),
			m.theme.muted.Render("change an uploaded file. Its proposals"),
			m.theme.muted.Render("appear here: a accepts, r rejects, d"),
			m.theme.muted.Render("switches between diff and file view."),
		}
	} else {
		listRows := rows
		if sel >= 0 && entries[sel].Change.Reason != "" && entries[sel].Change.Status != edits.StatusAccepted && listRows > 3 {
			listRows -= 2
		}
		keepVisible(&m.chListTop, sel, len(entries), listRows)
		for i := m.chListTop; i < len(entries) && i < m.chListTop+listRows; i++ {
			left = append(left, m.changeRow(entries[i], i == sel, leftW))
		}
		if listRows < rows {
			for len(left) < listRows {
				left = append(left, "")
			}
			left = append(left, "", m.theme.fileNote.Render("! "+entries[sel].Change.Reason))
		}
	}
	var detail []string
	if sel >= 0 && (rightW > 0 || m.chFocus) {
		w := rightW
		if w <= 0 {
			w = m.width
		}
		detail = m.changeDetail(entries[sel], w, rows)
	}
	var panes string
	switch {
	case rightW > 0:
		panes = joinPanes(left, detail, leftW, rightW, rows)
	case m.chFocus && detail != nil:
		panes = joinPanes(detail, nil, m.width, 0, rows)
	default:
		panes = joinPanes(left, nil, m.width, 0, rows)
	}
	parts := []string{header, summaryLine, panes}
	if dl != "" {
		parts = append(parts, dl)
	}
	return joinLines(append(parts, note)...)
}

// downloadBlock is the scp/sftp commands for the exports.
func (m *Model) downloadBlock() string {
	lines := []string{m.theme.files.Render("Download (after x export, t whole folder)")}
	for _, c := range m.downloadCommands() {
		lines = append(lines, m.clipWidth(m.theme.text.Render("  "+c)))
	}
	if m.root() != "" {
		lines = append(lines, m.clipWidth(m.theme.muted.Render("  Run git apply inside "+m.root()+"/. Downloads are removed when your last session ends.")))
	} else {
		lines = append(lines, m.clipWidth(m.theme.muted.Render("  Downloads are removed when your last session ends.")))
	}
	return strings.Join(lines, "\n")
}

// root is the single uploaded top-level folder, if there is one.
func (m *Model) root() string {
	if m.files == nil || m.edits == nil {
		return ""
	}
	return edits.RootDir(uploadedNames(m.files, m.user, m.workspace()))
}

func (m *Model) changeRow(e edits.Entry, selected bool, width int) string {
	cur := "  "
	if selected {
		cur = "› "
	}
	tag := statusTag(e)
	kind := ""
	switch {
	case e.Change.Status == edits.StatusPending && e.Change.New():
		kind = " NEW"
	case e.Change.Status == edits.StatusPending && e.Change.Deletes():
		kind = " DEL"
	case e.New:
		kind = " NEW"
	case e.Deleted:
		kind = " DEL"
	}
	stats := ""
	if e.Change.Status == edits.StatusPending {
		stats = fmt.Sprintf("+%d -%d", e.Change.Added, e.Change.Removed)
	} else if e.Modified {
		stats = fmt.Sprintf("+%d -%d", e.Added, e.Removed)
	}
	if e.Modified && e.Change.Status == edits.StatusPending {
		stats += " ●"
	}
	label := fmt.Sprintf("%-10s", tag) + kind
	style := m.theme.text
	switch e.Change.Status {
	case edits.StatusPending:
		style = m.theme.accent
	case edits.StatusAccepted:
		style = m.theme.ok
	case edits.StatusFailed:
		style = m.theme.err
	case edits.StatusRejected, edits.StatusSuperseded:
		style = m.theme.muted
	}
	line := fitMiddle(max(1, width), cur+label+"  ", e.Path, "  "+stats)
	if selected && m.chFocus {
		style = style.Underline(true)
	}
	return style.Render(padTo(line, width))
}

// changeTexts is what the detail pane compares for e: old and new text
// and a title naming them.
func (m *Model) changeTexts(e edits.Entry) (old, new, title string, ok bool) {
	orig, newest, current, pending, ok := m.workspace().Compare(e.Path)
	if !ok {
		return "", "", "", false
	}
	switch {
	case pending && m.chAgainstCurrent:
		return current, newest, "accepted → proposed", true
	case pending:
		return orig, newest, "upload → proposed", true
	default:
		return orig, newest, "upload → accepted", true
	}
}

// diffBody renders the coloured unified diff and the index of each hunk
// header in the returned lines.
func (m *Model) diffBody(old, new string) ([]string, []int) {
	var body []string
	var heads []int
	for _, h := range edits.Hunks(old, new, 3) {
		heads = append(heads, len(body))
		body = append(body, m.theme.status.Render(h.Header()))
		for _, l := range h.Lines {
			l = strings.ReplaceAll(l, "\t", "    ")
			switch l[0] {
			case '+':
				body = append(body, m.theme.ok.Render(l))
			case '-':
				body = append(body, m.theme.err.Render(l))
			default:
				body = append(body, m.theme.muted.Render(l))
			}
		}
	}
	if len(body) == 0 {
		body = append(body, m.theme.muted.Render("(no textual difference)"))
	}
	return body, heads
}

// changeDetail is the right pane: a title and the diff or the file.
func (m *Model) changeDetail(e edits.Entry, width, rows int) []string {
	old, new, title, ok := m.changeTexts(e)
	if !ok {
		return nil
	}
	var body []string
	if m.chFileView {
		title = strings.TrimPrefix(title[strings.Index(title, "→")+len("→"):], " ") + " version"
		if new == "" {
			body = []string{m.theme.muted.Render("(the file is deleted in this version)")}
		} else {
			for _, l := range numbered(new) {
				body = append(body, m.theme.text.Render(l))
			}
		}
	} else {
		added, removed := edits.Stats(old, new)
		title += fmt.Sprintf(" · +%d -%d", added, removed)
		body, _ = m.diffBody(old, new)
	}
	view := rows - 1
	if view < 1 {
		view = 1
	}
	shown := window(body, &m.chTop, view)
	span := ""
	if len(body) > view {
		span = fmt.Sprintf("  %d-%d/%d", m.chTop+1, m.chTop+len(shown), len(body))
	}
	head := m.theme.files.Render(padTo(fitMiddle(width, "", e.Path+" · "+title, span), width))
	return append([]string{head}, shown...)
}

// jumpHunk scrolls the diff to the next or previous hunk.
func (m *Model) jumpHunk(next bool) {
	if m.chFileView {
		return
	}
	entries := m.changeEntries()
	i := m.changeIndex(entries)
	if i < 0 {
		return
	}
	old, new, _, ok := m.changeTexts(entries[i])
	if !ok {
		return
	}
	_, heads := m.diffBody(old, new)
	if next {
		for _, h := range heads {
			if h > m.chTop {
				m.chTop = h
				return
			}
		}
		return
	}
	for j := len(heads) - 1; j >= 0; j-- {
		if heads[j] < m.chTop {
			m.chTop = heads[j]
			return
		}
	}
	m.chTop = 0
}

func (m *Model) changesHelp(short bool) []string {
	if short {
		return []string{"a/r", "A/R", "d diff/file", "tab", "]/[", "u", "v", "f", "x", "t", "m", "esc", "ctrl+q"}
	}
	view := "d file view"
	if m.chFileView {
		view = "d diff view"
	}
	return []string{"a/r accept/reject", "A/R all", view, "b diff base", "tab/enter scroll pane", "]/[ hunks", "u undo", "v revert", "f send failures", "x export", "t whole folder .tar.gz", "m edit mode", "esc chat", "ctrl+q exit"}
}
