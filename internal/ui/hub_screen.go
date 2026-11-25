package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/diskspace"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/hub"
	"github.com/UnixEU/ssh-llm-tui/internal/pull"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The Docker Hub screen (p on the model list): search the namespace's
// repositories, pick a tag, check the free space on the model disk and
// queue a background pull. The queue is shared by every session.

type hubFocus int

const (
	hubSearch hubFocus = iota
	hubRepos
	hubTags
	hubJobs
)

type hubState struct {
	query    textinput.Model
	focus    hubFocus
	loading  bool
	err      string
	repos    []hub.Repo
	shown    []hub.Repo
	repoSel  int
	repoTop  int
	tags     map[string][]hub.Tag
	tagErr   map[string]string
	tagsFor  string // repo whose tags are loading
	tagSel   int
	tagTop   int
	jobSel   int
	confirm  *hubConfirm
	note     string
	finished int
	ticking  bool
}

type hubConfirm struct {
	ref       string
	space     pull.Space
	diskErr   string
	installed bool
}

type hubReposMsg struct {
	repos []hub.Repo
	err   error
}

type hubTagsMsg struct {
	repo string
	tags []hub.Tag
	err  error
}

type hubTickMsg struct{}

// hubReady reports whether the Docker Hub screen is available.
func (m *Model) hubReady() bool {
	return m.hubc != nil && m.pulls != nil && m.client != nil
}

// activePulls counts queued and running pulls of every user.
func (m *Model) activePulls() int {
	if !m.hubReady() {
		return 0
	}
	n := 0
	for _, j := range m.pulls.Jobs() {
		if !j.State.Finished() {
			n++
		}
	}
	return n
}

func (m *Model) openHub() (tea.Model, tea.Cmd) {
	if !m.hubReady() {
		return m, nil
	}
	if m.hub == nil {
		q := textinput.New()
		q.Prompt = "Search " + m.hubc.Namespace() + "/ › "
		q.Placeholder = "name, e.g. qwen or gemma"
		q.PromptStyle = m.theme.accent
		q.TextStyle = m.theme.text
		q.PlaceholderStyle = m.theme.muted
		q.CharLimit = 64
		m.hub = &hubState{query: q, tags: map[string][]hub.Tag{}, tagErr: map[string]string{}}
	}
	h := m.hub
	h.confirm = nil
	h.note = ""
	h.focus = hubSearch
	h.finished = m.pulls.Finished()
	m.screen = screenHub
	cmds := []tea.Cmd{h.query.Focus(), m.hubTick(), m.spinner.Tick}
	if len(h.repos) == 0 && !h.loading {
		h.loading = true
		h.err = ""
		cmds = append(cmds, m.fetchRepos())
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) closeHub() (tea.Model, tea.Cmd) {
	if m.hub != nil {
		m.hub.query.Blur()
		m.hub.confirm = nil
	}
	return m.openModels()
}

func (m *Model) fetchRepos() tea.Cmd {
	c := m.hubc
	ctx := m.session
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		repos, err := c.Repos(ctx)
		return hubReposMsg{repos: repos, err: err}
	}
}

func (m *Model) fetchTags(repo string) tea.Cmd {
	c := m.hubc
	ctx := m.session
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		tags, err := c.Tags(ctx, repo)
		return hubTagsMsg{repo: repo, tags: tags, err: err}
	}
}

// hubTick refreshes the progress bars once a second while the screen is
// open. Only one tick chain runs.
func (m *Model) hubTick() tea.Cmd {
	if m.hub == nil || m.hub.ticking {
		return nil
	}
	m.hub.ticking = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return hubTickMsg{} })
}

func (m *Model) onHubTick() (tea.Model, tea.Cmd) {
	h := m.hub
	if h == nil {
		return m, nil
	}
	h.ticking = false
	if m.screen != screenHub {
		return m, nil
	}
	if n := m.pulls.Finished(); n != h.finished {
		h.finished = n
		for _, j := range m.pulls.Jobs() {
			if j.State == pull.Done && time.Since(j.Finished) < 2*time.Second {
				h.note = "Pulled " + j.Ref + ". It is in the model list now."
			}
		}
	}
	return m, m.hubTick()
}

func (m *Model) onHubRepos(msg hubReposMsg) (tea.Model, tea.Cmd) {
	h := m.hub
	if h == nil {
		return m, nil
	}
	h.loading = false
	if msg.err != nil {
		h.err = oneLine(msg.err)
		return m, nil
	}
	h.err = ""
	h.repos = msg.repos
	m.filterRepos()
	return m, nil
}

func (m *Model) onHubTags(msg hubTagsMsg) (tea.Model, tea.Cmd) {
	h := m.hub
	if h == nil {
		return m, nil
	}
	if h.tagsFor == msg.repo {
		h.tagsFor = ""
	}
	if msg.err != nil {
		h.tagErr[msg.repo] = oneLine(msg.err)
		return m, nil
	}
	delete(h.tagErr, msg.repo)
	h.tags[msg.repo] = msg.tags
	return m, nil
}

func (m *Model) filterRepos() {
	h := m.hub
	h.shown = hub.Search(h.repos, h.query.Value())
	h.repoSel, h.repoTop = 0, 0
	h.tagSel, h.tagTop = 0, 0
}

func (m *Model) selectedRepo() (hub.Repo, bool) {
	h := m.hub
	if h == nil || h.repoSel < 0 || h.repoSel >= len(h.shown) {
		return hub.Repo{}, false
	}
	return h.shown[h.repoSel], true
}

// loadTags starts fetching the selected repo's tags unless they are known.
func (m *Model) loadTags() tea.Cmd {
	r, ok := m.selectedRepo()
	if !ok {
		return nil
	}
	h := m.hub
	if _, ok := h.tags[r.Name]; ok || h.tagsFor == r.Name {
		return nil
	}
	h.tagsFor = r.Name
	return m.fetchTags(r.Name)
}

func (m *Model) onHubKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := m.hub
	if h == nil {
		return m.openModels()
	}
	key := msg.String()
	if h.confirm != nil {
		return m.onHubConfirmKey(key)
	}
	if h.focus == hubSearch {
		switch key {
		case "esc":
			if h.query.Value() != "" {
				h.query.SetValue("")
				m.filterRepos()
				return m, nil
			}
			return m.closeHub()
		case "enter", "down", "tab":
			h.query.Blur()
			h.focus = hubRepos
			return m, m.loadTags()
		}
		before := h.query.Value()
		var cmd tea.Cmd
		h.query, cmd = h.query.Update(msg)
		if h.query.Value() != before {
			m.filterRepos()
		}
		return m, cmd
	}
	rows := max(3, m.paneRows)
	switch key {
	case "esc", "left", "h":
		switch h.focus {
		case hubTags, hubJobs:
			h.focus = hubRepos
			return m, nil
		}
		if key == "esc" {
			return m.closeHub()
		}
		return m, nil
	case "q":
		return m.closeHub()
	case "/":
		h.focus = hubSearch
		return m, h.query.Focus()
	case "tab":
		switch h.focus {
		case hubRepos:
			h.focus = hubTags
			return m, m.loadTags()
		case hubTags:
			if len(m.pulls.Jobs()) > 0 {
				h.focus = hubJobs
			} else {
				h.focus = hubRepos
			}
		default:
			h.focus = hubRepos
		}
		return m, nil
	case "enter", "right", "l":
		switch h.focus {
		case hubRepos:
			if _, ok := m.selectedRepo(); ok {
				h.focus = hubTags
				h.tagSel, h.tagTop = 0, 0
				return m, m.loadTags()
			}
		case hubTags:
			if key == "enter" {
				m.startConfirm()
			}
		}
		return m, nil
	case "c", "x", "delete":
		if h.focus == hubJobs {
			m.cancelJob()
		}
		return m, nil
	case "r":
		h.note = ""
		if h.err != "" && !h.loading {
			h.loading = true
			h.err = ""
			return m, m.fetchRepos()
		}
		if r, ok := m.selectedRepo(); ok && h.tagErr[r.Name] != "" {
			delete(h.tagErr, r.Name)
			return m, m.loadTags()
		}
		return m, nil
	}
	delta := 0
	switch key {
	case "up", "k":
		delta = -1
	case "down", "j":
		delta = 1
	case "pgup", "ctrl+u":
		delta = -scrollStep(rows)
	case "pgdown", "ctrl+d", " ":
		delta = scrollStep(rows)
	case "home", "g":
		delta = -1 << 30
	case "end", "G":
		delta = 1 << 30
	default:
		return m, nil
	}
	switch h.focus {
	case hubRepos:
		h.repoSel = clampSel(h.repoSel+delta, len(h.shown))
		h.tagSel, h.tagTop = 0, 0
		return m, m.loadTags()
	case hubTags:
		r, _ := m.selectedRepo()
		h.tagSel = clampSel(h.tagSel+delta, len(h.tags[r.Name]))
	case hubJobs:
		h.jobSel = clampSel(h.jobSel+delta, len(m.pulls.Jobs()))
	}
	return m, nil
}

func clampSel(i, n int) int {
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}

// startConfirm opens the pull check for the selected tag: download size,
// what queued pulls still need, the reserve and the free space on the
// model disk.
func (m *Model) startConfirm() {
	h := m.hub
	r, ok := m.selectedRepo()
	if !ok {
		return
	}
	tags := h.tags[r.Name]
	if h.tagSel < 0 || h.tagSel >= len(tags) {
		return
	}
	t := tags[h.tagSel]
	c := &hubConfirm{ref: m.hubc.Ref(r.Name, t.Name)}
	c.space = pull.Space{Size: t.Size, Pending: m.pulls.Pending(), Reserve: m.cfg.Hub.MinFree}
	if info, err := m.readModelDisk(); err != nil {
		c.diskErr = oneLine(err)
	} else {
		c.space.Known = true
		c.space.Free = info.FreeBytes
	}
	c.installed = m.installed(c.ref)
	h.confirm = c
	h.note = ""
}

func (m *Model) readModelDisk() (diskspace.Info, error) {
	if m.diskFree != nil {
		return m.diskFree()
	}
	dir := m.cfg.ModelsDir()
	if dir == "" {
		return diskspace.Info{}, errors.New("MODEL_STORE_PATH is not set, so the model disk cannot be read")
	}
	return diskspace.Read(dir)
}

func (m *Model) installed(ref string) bool {
	for _, md := range m.models {
		if dmr.SameModel(md.ID, ref) || dmr.SameModel(md.ID, strings.TrimSuffix(ref, ":latest")) {
			return true
		}
	}
	return false
}

func (m *Model) onHubConfirmKey(key string) (tea.Model, tea.Cmd) {
	h := m.hub
	c := h.confirm
	switch key {
	case "y", "Y", "enter":
		if !c.space.OK() {
			return m, nil
		}
		if _, err := m.pulls.Enqueue(m.user, c.ref, c.space.Size); err != nil {
			h.note = err.Error()
		} else {
			h.note = "Queued " + c.ref + ". The download runs in the background; you can leave this screen."
			h.focus = hubJobs
			h.jobSel = len(m.pulls.Jobs()) - 1
		}
		h.confirm = nil
		return m, m.hubTick()
	case "n", "N", "esc", "q":
		h.confirm = nil
	}
	return m, nil
}

func (m *Model) cancelJob() {
	h := m.hub
	jobs := m.pulls.Jobs()
	if h.jobSel < 0 || h.jobSel >= len(jobs) {
		return
	}
	j := jobs[h.jobSel]
	switch {
	case j.State.Finished():
		h.note = j.Ref + " is already " + j.State.String() + "."
	case j.User != m.user:
		h.note = "Only " + j.User + " can cancel " + j.Ref + "."
	case m.pulls.Cancel(j.ID):
		h.note = "Cancelled " + j.Ref + "."
	}
}

func (m *Model) viewHub(bodyH int) string {
	h := m.hub
	right := ""
	if info, err := m.readModelDisk(); err == nil {
		right = "model disk: " + config.FormatBytes(info.FreeBytes) + " free"
	}
	header := m.fitHeader("Docker Hub · "+m.hubc.Namespace()+"/", right, 2)
	search := m.clipWidth(h.query.View())
	count := ""
	switch {
	case h.loading:
		count = m.theme.spinner.Render(m.spinner.View()+" ") + m.theme.status.Render("Reading Docker Hub…")
	case h.err != "":
		count = m.theme.err.Render(h.err + " · r retries")
	default:
		count = m.theme.muted.Render(fmt.Sprintf("%d of %d repositories", len(h.shown), len(h.repos)))
	}
	jobs := m.pulls.Jobs()
	jobLines := m.jobLines(jobs, bodyH)
	note := ""
	if h.note != "" {
		note = m.clipWidth(m.theme.status.Render(h.note))
	}
	rows := bodyH - lineCount(header) - 2 - len(jobLines)
	if note != "" {
		rows--
	}
	if rows < 3 {
		rows = 3
	}
	m.paneRows = rows
	leftW, rightW := paneWidths(m.width)
	left := m.repoLines(leftW, rows)
	var detail []string
	if rightW > 0 || h.focus == hubTags || h.confirm != nil {
		w := rightW
		if w <= 0 {
			w = m.width
		}
		if h.confirm != nil {
			detail = m.confirmLines(w)
		} else {
			detail = m.tagLines(w, rows)
		}
	}
	var panes string
	switch {
	case rightW > 0:
		panes = joinPanes(left, detail, leftW, rightW, rows)
	case detail != nil:
		panes = joinPanes(detail, nil, m.width, 0, rows)
	default:
		panes = joinPanes(left, nil, m.width, 0, rows)
	}
	parts := []string{header, search, m.clipWidth(count), panes}
	parts = append(parts, jobLines...)
	return joinLines(append(parts, note)...)
}

func (m *Model) repoLines(width, rows int) []string {
	h := m.hub
	if len(h.shown) == 0 {
		if h.loading || h.err != "" {
			return nil
		}
		return []string{m.theme.muted.Render("No repository matches.")}
	}
	keepVisible(&h.repoTop, h.repoSel, len(h.shown), rows)
	var out []string
	for i := h.repoTop; i < len(h.shown) && i < h.repoTop+rows; i++ {
		r := h.shown[i]
		cur := "  "
		style := m.theme.text
		if i == h.repoSel {
			cur = "› "
			style = m.theme.accent
			if h.focus != hubRepos {
				style = m.theme.subtitle
			}
		}
		line := fitMiddle(width, cur, r.Name, "  "+pullCount(r.Pulls)+" pulls")
		out = append(out, style.Render(padTo(line, width)))
	}
	return out
}

func (m *Model) tagLines(width, rows int) []string {
	h := m.hub
	r, ok := m.selectedRepo()
	if !ok {
		return nil
	}
	ref := m.hubc.Namespace() + "/" + r.Name
	out := []string{m.theme.files.Render(padTo(ref, width))}
	if d := r.Description; d != "" {
		for i, l := range wrapText(d, width) {
			if i == 2 {
				break
			}
			out = append(out, m.theme.muted.Render(l))
		}
	}
	tags, have := h.tags[r.Name]
	switch {
	case h.tagErr[r.Name] != "":
		return append(out, m.theme.err.Render(h.tagErr[r.Name]), m.theme.muted.Render("r retries"))
	case !have:
		return append(out, m.theme.spinner.Render(m.spinner.View()+" ")+m.theme.status.Render("Reading tags…"))
	case len(tags) == 0:
		return append(out, m.theme.muted.Render("No tags the llama.cpp backend can run."))
	}
	listRows := rows - len(out)
	if listRows < 1 {
		listRows = 1
	}
	keepVisible(&h.tagTop, h.tagSel, len(tags), listRows)
	for i := h.tagTop; i < len(tags) && i < h.tagTop+listRows; i++ {
		t := tags[i]
		cur := "  "
		style := m.theme.text
		if i == h.tagSel && h.focus == hubTags {
			cur = "› "
			style = m.theme.accent
		}
		mark := ""
		if m.installed(m.hubc.Ref(r.Name, t.Name)) {
			mark = "  ✓ pulled"
		}
		size := "size ?"
		if t.Size > 0 {
			size = config.FormatBytes(t.Size)
		}
		out = append(out, style.Render(padTo(fitMiddle(width, cur, t.Name, "  "+size+mark), width)))
	}
	return out
}

func (m *Model) confirmLines(width int) []string {
	c := m.hub.confirm
	s := c.space
	row := func(label, value string) string {
		return m.theme.text.Render(padTo(fmt.Sprintf("  %-20s %s", label, value), width))
	}
	out := []string{m.theme.files.Render(padTo("Pull "+c.ref, width)), ""}
	size := "unknown"
	if s.Size > 0 {
		size = config.FormatBytes(s.Size)
	}
	out = append(out, row("Download size", size))
	if s.Pending > 0 {
		out = append(out, row("Queued pulls", config.FormatBytes(s.Pending)))
	}
	out = append(out, row("Kept free", config.FormatBytes(s.Reserve)), row("Needed", config.FormatBytes(s.Need())))
	if s.Known {
		out = append(out, row("Free on model disk", config.FormatBytes(s.Free)))
	} else {
		out = append(out, row("Free on model disk", "unknown"))
	}
	out = append(out, "")
	if c.installed {
		out = append(out, m.theme.muted.Render("Already pulled. Pulling again only fetches what changed."))
	}
	switch {
	case !s.Known:
		for _, l := range wrapText(c.diskErr, width) {
			out = append(out, m.theme.fileNote.Render(l))
		}
		out = append(out, m.theme.accent.Render("y pulls anyway · n cancels"))
	case !s.OK():
		out = append(out,
			m.theme.err.Render(fmt.Sprintf("Not enough space: %s short.", config.FormatBytes(s.Need()-s.Free))),
			m.theme.muted.Render("Free space on the model disk or wait for queued pulls. n goes back."))
	case s.Size <= 0:
		out = append(out, m.theme.fileNote.Render("The Hub did not report a size."), m.theme.accent.Render("y pulls anyway · n cancels"))
	default:
		out = append(out, m.theme.ok.Render("Enough space."), m.theme.accent.Render("y queues the pull · n cancels"))
	}
	return out
}

// jobLines is the downloads strip: a title and one line per pull, at most
// a third of the screen.
func (m *Model) jobLines(jobs []pull.Job, bodyH int) []string {
	if len(jobs) == 0 {
		return nil
	}
	h := m.hub
	maxRows := bodyH / 3
	if maxRows < 1 {
		maxRows = 1
	}
	title := fmt.Sprintf("Downloads (%d)", len(jobs))
	if h.focus == hubJobs {
		title += " · c cancels your pull"
	}
	out := []string{m.clipWidth(m.theme.files.Render(title))}
	h.jobSel = clampSel(h.jobSel, len(jobs))
	top := 0
	keepVisible(&top, h.jobSel, len(jobs), maxRows)
	for i := top; i < len(jobs) && i < top+maxRows; i++ {
		out = append(out, m.jobLine(jobs[i], i == h.jobSel && h.focus == hubJobs))
	}
	return out
}

func (m *Model) jobLine(j pull.Job, selected bool) string {
	cur := "  "
	if selected {
		cur = "› "
	}
	nameW := 28
	if m.width < 90 {
		nameW = 18
	}
	name := padTo(j.Ref, nameW)
	barW := 20
	if m.width < 90 {
		barW = 10
	}
	frac := j.Fraction()
	bar := progressBar(frac, barW)
	pct := "  ?%"
	if frac >= 0 {
		pct = fmt.Sprintf("%3.0f%%", frac*100)
	}
	info := ""
	switch j.State {
	case pull.Running:
		info = config.FormatBytes(j.Done)
		if e := j.Expected(); e > 0 {
			info += "/" + config.FormatBytes(e)
		}
		if j.Rate > 0 {
			info += " · " + config.FormatBytes(int64(j.Rate)) + "/s"
		}
	case pull.Queued:
		if j.Size > 0 {
			info = config.FormatBytes(j.Size)
		}
	case pull.Failed:
		info = j.Err
	}
	line := fmt.Sprintf("%s%s %s %s  %-9s %s · %s", cur, name, bar, pct, j.State, info, j.User)
	style := m.theme.text
	switch j.State {
	case pull.Done:
		style = m.theme.ok
	case pull.Failed:
		style = m.theme.err
	case pull.Cancelled, pull.Queued:
		style = m.theme.muted
	}
	if selected {
		style = style.Bold(true)
	}
	return style.Render(padTo(line, m.width))
}

// progressBar draws frac (0-1) in width cells; -1 is unknown.
func progressBar(frac float64, width int) string {
	if width < 1 {
		return ""
	}
	if frac < 0 {
		return strings.Repeat("·", width)
	}
	full := int(frac*float64(width) + 0.5)
	if full > width {
		full = width
	}
	return strings.Repeat("█", full) + strings.Repeat("░", width-full)
}

// pullCount renders 1906975 as 1.9M.
func pullCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// wrapText breaks s into lines of at most width cells at spaces.
func wrapText(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case lipgloss.Width(line)+1+lipgloss.Width(w) <= width:
			line += " " + w
		default:
			out = append(out, line)
			line = w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	for i, l := range out {
		out[i] = padTo(l, width)
	}
	return out
}

func (m *Model) hubHelp(short bool) []string {
	h := m.hub
	if h != nil && h.confirm != nil {
		if short {
			return []string{"y", "n", "ctrl+q"}
		}
		return []string{"y queue the pull", "n back", "ctrl+q exit"}
	}
	if h != nil && h.focus == hubSearch {
		if short {
			return []string{"type", "enter", "esc", "ctrl+q"}
		}
		return []string{"type to search", "enter/↓ results", "esc clear, then back", "ctrl+q exit"}
	}
	if short {
		return []string{"/", "enter", "tab", "c", "esc", "ctrl+q"}
	}
	return []string{"/ search", "j/k move", "enter tags, then pull", "tab repos/tags/downloads", "c cancel your pull", "r retry", "esc back", "ctrl+q exit"}
}
