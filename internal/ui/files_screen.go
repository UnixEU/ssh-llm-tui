package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The ctrl+f files screen: the upload tree on the left, a preview of the
// selected file or folder on the right.

func (m *Model) openFiles() (tea.Model, tea.Cmd) {
	if m.files == nil {
		return m, nil
	}
	m.screen = screenFiles
	m.fileConfirm = false
	m.fileRight = false
	m.previewTop = 0
	m.status = ""
	m.ensureFileSel()
	return m, nil
}

func (m *Model) closeFiles() (tea.Model, tea.Cmd) {
	m.screen = screenChat
	m.fileConfirm = false
	m.status = ""
	m.syncTranscript(true)
	return m, m.input.Focus()
}

// filesPaneRows is the list height on the files screen. View sets it.
func (m *Model) filesPaneRows() int {
	if m.paneRows > 0 {
		return m.paneRows
	}
	return m.filesBoxRows()
}

func (m *Model) onFilesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.fileConfirm {
		switch key {
		case "y", "Y":
			_ = m.files.Clear(m.user)
			if m.edits != nil {
				m.edits.Clear(m.user)
			}
			m.fileSel = ""
			m.status = "Cleared every uploaded file."
			m.fileConfirm = false
		case "n", "N", "esc":
			m.fileConfirm = false
		}
		return m, nil
	}
	rows := m.filesPaneRows()
	switch key {
	case "esc", "ctrl+f", "q":
		if m.fileRight && key == "esc" {
			m.fileRight = false
			return m, nil
		}
		return m.closeFiles()
	case "tab", "enter":
		m.fileRight = !m.fileRight
	case "right", "l":
		m.fileRight = true
	case "left", "h":
		m.fileRight = false
	case "up", "k":
		if m.fileRight {
			m.previewTop--
		} else {
			m.moveFile(-1)
			m.previewTop = 0
		}
	case "down", "j":
		if m.fileRight {
			m.previewTop++
		} else {
			m.moveFile(1)
			m.previewTop = 0
		}
	case "pgup", "ctrl+u":
		if m.fileRight {
			m.previewTop -= scrollStep(rows)
		} else {
			m.moveFile(-rows)
			m.previewTop = 0
		}
	case "pgdown", "ctrl+d", " ":
		if key == " " && !m.fileRight {
			m.toggleSelectedFile()
			return m, nil
		}
		if m.fileRight {
			m.previewTop += scrollStep(rows)
		} else {
			m.moveFile(rows)
			m.previewTop = 0
		}
	case "home", "g":
		if m.fileRight {
			m.previewTop = 0
		} else {
			m.jumpFile(-1)
		}
	case "end", "G":
		if m.fileRight {
			m.previewTop = 1 << 30
		} else {
			m.jumpFile(1)
		}
	case "a", "n":
		on := key == "a"
		if n, err := m.files.SetIncludedDir(m.user, "", on); err == nil {
			verb := "Excluded"
			if on {
				verb = "Included"
			}
			m.status = verb + " " + plural(n, "file", "files") + "."
		}
	case "d", "x":
		m.removeSelectedFile()
		m.previewTop = 0
	case "D":
		if len(m.fileItems()) > 0 {
			m.fileConfirm = true
		}
	case "e":
		if m.editsReady() {
			if p := strings.TrimSuffix(m.fileSel, "/"); p != "" && p == m.fileSel {
				m.chPath = p
			}
			return m.openChanges()
		}
	}
	if m.previewTop < 0 {
		m.previewTop = 0
	}
	return m, nil
}

// uploadQuota is "4.2 MiB of 256 MiB".
func (m *Model) uploadQuota() string {
	_, used := m.files.Usage(m.user)
	return humanSize(used) + " of " + humanSize(m.files.MaxTotalBytes())
}

func (m *Model) viewFiles(bodyH int) string {
	files := m.files.List(m.user)
	items := buildFileItems(files, m.files.Dirs(m.user))
	incl := 0
	for _, f := range files {
		if f.Included {
			incl++
		}
	}
	header := m.fitHeader("Files", fmt.Sprintf("%d files · %d included", len(files), incl), 2)
	p, _, target := m.sshTarget()
	info := fmt.Sprintf("%s · prompt %s · upload: scp %s-r dir %s:uploads/", m.uploadQuota(), tokenGauge(includedTokens(files), m.budget), p, target)
	infoLine := m.clipWidth(m.theme.muted.Render(info))
	status := ""
	switch {
	case m.fileConfirm:
		status = m.theme.err.Render("Delete every uploaded file and the edits? y/n")
	case m.status != "":
		status = m.theme.status.Render(m.status)
	}
	used := lineCount(header) + 1 + 1
	if status != "" {
		used++
	}
	rows := bodyH - used
	if rows < 1 {
		rows = 1
	}
	m.paneRows = rows
	leftW, rightW := paneWidths(m.width)
	var left []string
	if len(items) == 0 {
		left = []string{m.theme.muted.Render("No files yet."), "", m.theme.muted.Render("Upload with scp or sftp:"), m.theme.text.Render("scp " + p + "-r dir " + target + ":uploads/")}
	} else {
		sel := m.fileIndex(items)
		keepVisible(&m.fileTop, sel, len(items), rows)
		end := m.fileTop + rows
		if end > len(items) {
			end = len(items)
		}
		for i := m.fileTop; i < end; i++ {
			row := m.fileRowW(items[i], i, len(items), rows, leftW)
			if items[i].key == m.fileSel {
				if m.fileRight && rightW > 0 {
					row = m.theme.accent.Render(row)
				} else {
					row = m.theme.files.Render(row)
				}
			}
			left = append(left, row)
		}
	}
	var right []string
	if rightW > 0 || m.fileRight {
		w := rightW
		if w <= 0 {
			w = m.width
		}
		right = m.filePreview(items, w, rows)
	}
	var panes string
	if rightW > 0 {
		panes = joinPanes(left, right, leftW, rightW, rows)
	} else if m.fileRight {
		panes = joinPanes(right, nil, m.width, 0, rows)
	} else {
		panes = joinPanes(left, nil, m.width, 0, rows)
	}
	return joinLines(header, infoLine, panes, status)
}

// filePreview is the right pane: a title line and the scrolled content.
func (m *Model) filePreview(items []fileItem, width, rows int) []string {
	i := m.fileIndex(items)
	if i < 0 {
		return []string{m.theme.muted.Render("Select a file to preview it.")}
	}
	it := items[i]
	var title string
	var body []string
	if it.dir {
		title = it.path + "/"
		body = []string{
			fmt.Sprintf("%s, %d included", plural(it.count, "file", "files"), it.incl),
			fmt.Sprintf("%s, ~%st included", humanSize(it.size), shortCount(it.tokens)),
			"",
			"space includes or excludes everything below, d removes the folder.",
		}
	} else {
		f := it.file
		state := "excluded"
		if f.Included {
			state = "included"
		}
		title = fmt.Sprintf("%s · %s · %s", f.Name, humanSize(f.Size), state)
		text := f.Text
		edited := false
		if m.edits != nil {
			if cur, exists, ok := m.workspace().Version(f.Name); ok {
				edited = true
				if !exists {
					text = ""
					title += " · deleted in the edits"
				} else {
					text = cur
				}
			}
		}
		if edited {
			title += " · edited ● (e diff)"
		}
		switch {
		case f.IsImage:
			body = append(body, fmt.Sprintf("Image %dx%d, %s.", f.Width, f.Height, nonEmpty(f.MIME, "image")))
			if f.Sent {
				body = append(body, "Sent with an earlier message. Include it again to send it once more.")
			} else {
				body = append(body, "Sent with the next message while included.")
			}
		case strings.TrimSpace(text) == "":
			body = []string{"(no text)"}
		default:
			title += fmt.Sprintf(" · ~%dt", f.Tokens)
			body = numbered(text)
		}
		if f.Note != "" {
			body = append([]string{m.theme.fileNote.Render("! " + strings.Join(strings.Fields(f.Note), " ")), ""}, body...)
		}
	}
	view := rows - 1
	if view < 1 {
		view = 1
	}
	shown := window(body, &m.previewTop, view)
	span := ""
	if len(body) > view {
		span = fmt.Sprintf("  %d-%d/%d", m.previewTop+1, m.previewTop+len(shown), len(body))
	}
	head := m.theme.files.Render(padTo(fitMiddle(width, "", title, span), width))
	out := []string{head}
	for _, l := range shown {
		out = append(out, m.theme.text.Render(l))
	}
	return out
}

func (m *Model) filesHelp(short bool) []string {
	if m.fileConfirm {
		return []string{"y delete all", "n keep", "ctrl+q exit"}
	}
	if m.fileRight {
		if short {
			return []string{"j/k", "pgup", "g/G", "tab", "esc", "ctrl+q"}
		}
		return []string{"j/k pgup/pgdn scroll", "g/G top/end", "tab/esc back to list", "ctrl+f chat", "ctrl+q exit"}
	}
	if short {
		out := []string{"space", "a/n", "d", "D", "tab"}
		if m.editsReady() {
			out = append(out, "e")
		}
		return append(out, "esc", "ctrl+q")
	}
	out := []string{"space include", "a/n all/none", "d remove", "D clear all", "tab/enter preview"}
	if m.editsReady() {
		out = append(out, "e changes")
	}
	return append(out, "esc/ctrl+f chat", "ctrl+q exit")
}
