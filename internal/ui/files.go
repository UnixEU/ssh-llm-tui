package ui

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

// fileItem is one row of the file box: a directory header or a file.
// Directories are listed before their contents, so an uploaded tree reads
// like a file browser.
type fileItem struct {
	key   string // file path, or directory path with a trailing "/"
	path  string
	depth int
	dir   bool
	file  uploads.File
	// Directory totals over every file below it.
	count  int
	incl   int
	tokens int // tokens of included files
	size   int64
}

func dirKey(p string) string { return p + "/" }

// buildFileItems turns the flat store listing into tree rows.
func buildFileItems(files []uploads.File, dirs []string) []fileItem {
	byDir := map[string]*fileItem{}
	addDir := func(p string) {
		if p == "" || byDir[p] != nil {
			return
		}
		byDir[p] = &fileItem{key: dirKey(p), path: p, depth: strings.Count(p, "/"), dir: true}
	}
	for _, d := range dirs {
		for _, parent := range ancestors(d) {
			addDir(parent)
		}
		addDir(d)
	}
	items := make([]fileItem, 0, len(files)+len(byDir))
	for _, f := range files {
		for _, parent := range ancestors(f.Name) {
			addDir(parent)
			d := byDir[parent]
			d.count++
			d.size += f.Size
			if f.Included {
				d.incl++
				d.tokens += f.Tokens
			}
		}
		items = append(items, fileItem{key: f.Name, path: f.Name, depth: strings.Count(f.Name, "/"), file: f})
	}
	for _, d := range byDir {
		items = append(items, *d)
	}
	sort.Slice(items, func(i, j int) bool { return treeLess(items[i].path, items[j].path) })
	return items
}

// ancestors returns the parent directories of p, outermost first.
func ancestors(p string) []string {
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

// treeLess orders paths component by component, so "a/x" sorts right after
// "a" and before "a-b".
func treeLess(a, b string) bool {
	pa, pb := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

func (m *Model) fileItems() []fileItem {
	if m.files == nil {
		return nil
	}
	return buildFileItems(m.files.List(m.user), m.files.Dirs(m.user))
}

func (m *Model) fileIndex(items []fileItem) int {
	for i, it := range items {
		if it.key == m.fileSel {
			return i
		}
	}
	return -1
}

func (m *Model) ensureFileSel() {
	items := m.fileItems()
	if len(items) == 0 {
		m.fileSel = ""
		return
	}
	if m.fileIndex(items) >= 0 {
		return
	}
	// Keep the selection near where it was: the next row after the old key.
	for _, it := range items {
		if m.fileSel != "" && treeLess(strings.TrimSuffix(m.fileSel, "/"), it.path) {
			m.fileSel = it.key
			return
		}
	}
	if m.fileSel != "" {
		m.fileSel = items[len(items)-1].key
		return
	}
	m.fileSel = items[0].key
}

func (m *Model) moveFile(delta int) {
	items := m.fileItems()
	if len(items) == 0 {
		return
	}
	m.ensureFileSel()
	i := m.fileIndex(items)
	if i < 0 {
		i = 0
	}
	i += delta
	if i < 0 {
		i = 0
	}
	if i >= len(items) {
		i = len(items) - 1
	}
	m.fileSel = items[i].key
	m.revealFile(i, len(items))
}

// jumpFile moves the selection to the first (dir < 0) or last row.
func (m *Model) jumpFile(dir int) {
	items := m.fileItems()
	if len(items) == 0 {
		return
	}
	i := 0
	if dir > 0 {
		i = len(items) - 1
	}
	m.fileSel = items[i].key
	m.revealFile(i, len(items))
}

func (m *Model) pageFiles(dir int) {
	items := m.fileItems()
	if len(items) == 0 {
		return
	}
	rows := m.filesBoxRows()
	m.fileTop += dir * rows
	maxTop := len(items) - rows
	if maxTop < 0 {
		maxTop = 0
	}
	if m.fileTop < 0 {
		m.fileTop = 0
	}
	if m.fileTop > maxTop {
		m.fileTop = maxTop
	}
	m.ensureFileSel()
	i := m.fileIndex(items)
	if i < m.fileTop || i >= m.fileTop+rows {
		i = m.fileTop
		if dir < 0 {
			i = m.fileTop + rows - 1
			if i >= len(items) {
				i = len(items) - 1
			}
		}
		m.fileSel = items[i].key
	}
}

// toggleSelectedFile flips one file, or every file under the selected
// directory: all of them are included unless all already were.
func (m *Model) toggleSelectedFile() {
	if m.files == nil || m.fileSel == "" {
		return
	}
	items := m.fileItems()
	i := m.fileIndex(items)
	if i < 0 {
		return
	}
	it := items[i]
	if it.dir {
		on := it.incl < it.count
		if n, err := m.files.SetIncludedDir(m.user, it.path, on); err == nil {
			verb := "Excluded"
			if on {
				verb = "Included"
			}
			m.status = fmt.Sprintf("%s %s under %s/.", verb, plural(n, "file", "files"), it.path)
		}
		return
	}
	_ = m.files.SetIncluded(m.user, it.path, !it.file.Included)
}

// removeSelectedFile deletes the selected file or directory tree.
func (m *Model) removeSelectedFile() {
	if m.files == nil || m.fileSel == "" {
		return
	}
	if strings.HasSuffix(m.fileSel, "/") {
		_ = m.files.RemoveDir(m.user, strings.TrimSuffix(m.fileSel, "/"))
	} else {
		_ = m.files.Remove(m.user, m.fileSel)
	}
	m.ensureFileSel()
}

func (m *Model) renderFileBox(focused bool) string {
	files := m.files.List(m.user)
	if !focused {
		return m.clipWidth(m.theme.muted.Render(fileSummary(files, m.budget)))
	}
	items := buildFileItems(files, m.files.Dirs(m.user))
	rows := m.filesBoxRows()
	if rows > len(items) {
		rows = len(items)
	}
	if rows < 1 {
		rows = 1
	}
	m.clampFileTop(len(items), rows)
	title := m.fileTitle(len(items), files)
	var lines []string
	lines = append(lines, m.clipWidth(m.theme.files.Render(title)))
	if len(items) == 0 {
		lines = append(lines, m.clipWidth(m.theme.muted.Render("(none yet)")))
		return strings.Join(lines, "\n")
	}
	end := m.fileTop + rows
	if end > len(items) {
		end = len(items)
	}
	for i := m.fileTop; i < end; i++ {
		lines = append(lines, m.fileRow(items[i], i, len(items), rows))
	}
	if note := selectedFileNote(files, m.fileSel); note != "" {
		lines = append(lines, m.clipWidth(m.theme.fileNote.Render("! "+note)))
	}
	return strings.Join(lines, "\n")
}

func selectedFileNote(files []uploads.File, key string) string {
	if key == "" || strings.HasSuffix(key, "/") {
		return ""
	}
	for _, f := range files {
		if f.Name != key || f.Note == "" {
			continue
		}
		return strings.Join(strings.Fields(f.Note), " ")
	}
	return ""
}

// includedTokens sums the token estimates of included files.
func includedTokens(files []uploads.File) int {
	n := 0
	for _, f := range files {
		if f.Included {
			n += f.Tokens
		}
	}
	return n
}

func fileSummary(files []uploads.File, budget int) string {
	incl, img := 0, 0
	for _, f := range files {
		if f.Included {
			incl++
		}
		if f.IsImage {
			img++
		}
	}
	return fmt.Sprintf("files: %d (%d incl, %s, %d img) — ctrl+f", len(files), incl, tokenGauge(includedTokens(files), budget), img)
}

// tokenGauge is "~used/budget t", with "over" when the included files do
// not fit and will be cut.
func tokenGauge(used, budget int) string {
	s := "~" + shortCount(used) + "/" + shortCount(budget) + "t"
	if budget > 0 && used > budget {
		s += " over"
	}
	return s
}

func shortCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 100_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
}

func (m *Model) fileTitle(n int, files []uploads.File) string {
	hint := "scp -P 2222 -r dir user@host:uploads/"
	extra := ""
	if m.fileConfirm {
		extra = "  clear all? y/n"
	}
	gauge := "  " + tokenGauge(includedTokens(files), m.budget)
	span := ""
	if n > 0 {
		rows := m.filesBoxRows()
		if rows > n {
			rows = n
		}
		span = fmt.Sprintf("  %d-%d/%d", m.fileTop+1, m.fileTop+rows, n)
	}
	return fitMiddle(m.width, "files  ", hint+gauge+span+extra, "")
}

func (m *Model) fileRow(it fileItem, index, total, rows int) string {
	cur := " "
	if it.key == m.fileSel {
		cur = ">"
	}
	depth := it.depth
	if depth > 6 {
		depth = 6
	}
	indent := strings.Repeat("  ", depth)
	var mark, name, info string
	if it.dir {
		switch {
		case it.count > 0 && it.incl == it.count:
			mark = "✓"
		case it.incl > 0:
			mark = "◐"
		default:
			mark = "✗"
		}
		name = path.Base(it.path) + "/"
		info = fmt.Sprintf("%d/%d incl  ~%st", it.incl, it.count, shortCount(it.tokens))
	} else {
		f := it.file
		mark = "✗"
		if f.Included {
			mark = "✓"
		}
		name = path.Base(f.Name)
		if f.IsImage {
			info = fmt.Sprintf("img %dx%d  %s", f.Width, f.Height, humanSize(f.Size))
			if f.Sent {
				info += "  sent"
			}
		} else {
			info = fmt.Sprintf("%s  ~%dt", humanSize(f.Size), f.Tokens)
			if f.Note != "" {
				info += " !"
			}
		}
	}
	left := cur + mark + " " + indent
	right := "  " + info + " " + scrollGlyph(index, m.fileTop, rows, total)
	line := fitMiddle(max(1, m.width), left, name, right)
	if strings.Contains(line, "\n") {
		line = strings.ReplaceAll(line, "\n", "")
	}
	return m.clipWidth(line)
}
