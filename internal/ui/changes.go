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
	m.filesFocus = false
	m.chDiff = false
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
	if m.chDiff {
		switch key {
		case "esc", "enter", "q":
			m.chDiff = false
			return m, nil
		case "up", "k":
			m.chTop--
		case "down", "j":
			m.chTop++
		case "pgup", "ctrl+u":
			m.chTop -= scrollStep(m.diffRows())
		case "pgdown", "ctrl+d", " ":
			m.chTop += scrollStep(m.diffRows())
		case "home", "g":
			m.chTop = 0
		case "end", "G":
			m.chTop = 1 << 30
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
	switch key {
	case "esc", "ctrl+e":
		m.screen = screenChat
		m.syncTranscript(true)
		return m, nil
	case "up", "k":
		m.moveChange(-1)
	case "down", "j":
		m.moveChange(1)
	case "home", "g":
		m.moveChange(-1 << 30)
	case "end", "G":
		m.moveChange(1 << 30)
	case "enter":
		if m.chPath != "" {
			m.chDiff = true
			m.chTop = 0
		}
	default:
		return m.onChangesAction(ws, key)
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
func (m *Model) diffRows() int {
	n := m.height - 12
	if n < 3 {
		n = 3
	}
	return n
}

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
	var lines []string
	if len(entries) == 0 {
		lines = append(lines, m.theme.muted.Render("No changes yet. Ask the model to change an uploaded file; its proposals appear here."))
	}
	pending, accepted := 0, 0
	for _, e := range entries {
		if e.Change.Status == edits.StatusPending {
			pending++
		}
		if e.Modified {
			accepted++
		}
	}
	listRows := len(entries)
	maxList := bodyH / 3
	if !m.chDiff {
		maxList = bodyH - 10
	}
	if maxList < 3 {
		maxList = 3
	}
	if listRows > maxList {
		listRows = maxList
	}
	sel := m.changeIndex(entries)
	top := 0
	if sel >= listRows {
		top = sel - listRows + 1
	}
	for i := top; i < len(entries) && i < top+listRows; i++ {
		lines = append(lines, m.changeRow(entries[i], i == sel))
	}
	if sel >= 0 {
		if note := entries[sel].Change.Reason; note != "" && entries[sel].Change.Status != edits.StatusAccepted {
			lines = append(lines, m.theme.fileNote.Render("! "+note))
		}
	}
	summary := fmt.Sprintf("%d waiting · %d changed against the upload", pending, accepted)
	if n := len(m.workspace().Failures()); n > 0 {
		summary += fmt.Sprintf(" · %s failed (f sends them back)", plural(n, "block", "blocks"))
	}
	lines = append(lines, m.theme.muted.Render(summary))
	if m.chNote != "" {
		lines = append(lines, m.theme.status.Render(m.chNote))
	}
	body := strings.Join(lines, "\n")
	var tail []string
	if m.chDiff && sel >= 0 {
		tail = append(tail, m.diffView(entries[sel], bodyH-lineCount(header)-lineCount(body)-1)...)
	} else {
		tail = append(tail, "", m.theme.files.Render("Download (after x export, t whole folder)"))
		for _, c := range m.downloadCommands() {
			tail = append(tail, m.theme.text.Render("  "+c))
		}
		if m.root() != "" {
			tail = append(tail, m.theme.muted.Render("  Run git apply inside "+m.root()+"/. Downloads are removed when your last session ends."))
		} else {
			tail = append(tail, m.theme.muted.Render("  Downloads are removed when your last session ends."))
		}
	}
	return joinLines(header, body, strings.Join(tail, "\n"))
}

// root is the single uploaded top-level folder, if there is one.
func (m *Model) root() string {
	if m.files == nil || m.edits == nil {
		return ""
	}
	return edits.RootDir(uploadedNames(m.files, m.user, m.workspace()))
}

func (m *Model) changeRow(e edits.Entry, selected bool) string {
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
	line := fitMiddle(max(1, m.width), cur+label+"  ", e.Path, "  "+stats)
	return style.Render(line)
}

// diffView renders the coloured diff of one file, scrolled to chTop.
func (m *Model) diffView(e edits.Entry, rows int) []string {
	if rows < 2 {
		rows = 2
	}
	old, new, title, ok := m.workspace().Diff(e.Path)
	if !ok {
		return nil
	}
	var body []string
	for _, h := range edits.Hunks(old, new, 3) {
		body = append(body, m.theme.status.Render(h.Header()))
		for _, l := range h.Lines {
			l = strings.ReplaceAll(l, "\t", "    ")
			l = m.clipWidth(l)
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
	maxTop := len(body) - (rows - 1)
	if maxTop < 0 {
		maxTop = 0
	}
	if m.chTop > maxTop {
		m.chTop = maxTop
	}
	end := m.chTop + rows - 1
	if end > len(body) {
		end = len(body)
	}
	head := m.theme.files.Render(fmt.Sprintf("%s · %s · lines %d-%d of %d", e.Path, title, m.chTop+1, end, len(body)))
	return append([]string{head}, body[m.chTop:end]...)
}

func (m *Model) changesHelp(short bool) []string {
	if m.chDiff {
		if short {
			return []string{"a", "r", "n/p", "pgup", "esc", "ctrl+q"}
		}
		return []string{"a accept", "r reject", "n/p next/prev file", "j/k pgup/pgdn scroll", "esc list", "ctrl+q exit"}
	}
	if short {
		return []string{"enter", "a/r", "A/R", "u", "v", "f", "x", "t", "m", "esc", "ctrl+q"}
	}
	return []string{"enter diff", "a/r accept/reject", "A/R all", "u undo", "v revert", "f send failures", "x export", "t whole folder .tar.gz", "m edit mode", "esc chat", "ctrl+q exit"}
}
