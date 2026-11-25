package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/diskspace"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/edits"
	"github.com/UnixEU/ssh-llm-tui/internal/fit"
	"github.com/UnixEU/ssh-llm-tui/internal/gpu"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/UnixEU/ssh-llm-tui/internal/hub"
	"github.com/UnixEU/ssh-llm-tui/internal/pull"
	"github.com/UnixEU/ssh-llm-tui/internal/sysmem"
	"github.com/UnixEU/ssh-llm-tui/internal/sysstat"
	"github.com/UnixEU/ssh-llm-tui/internal/think"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type screen int

const (
	screenWelcome screen = iota
	screenModels
	screenLoading
	screenChat
	screenContext
	screenTarget
	screenFit
	screenChanges
	screenCtxInfo
	screenFiles
	screenHub
)

type loadKind int

const (
	loadWarm loadKind = iota
	loadUnload
	loadProbe
)

type turnRole int

const (
	roleUser turnRole = iota
	roleAssistant
	roleNote
)

type turn struct {
	role          turnRole
	content       string
	reasoning     string
	reasoningOpen bool
	thinkFor      time.Duration
}

// SoleUnloader unloads DMR runners when this is the only TUI session.
type SoleUnloader interface {
	UnloadIfSole(ctx context.Context) (int, bool, error)
}

// Options is everything one SSH session needs to render the app.
type Options struct {
	Config   config.Config
	Client   *dmr.Client
	Gate     *gpu.Gate
	Renderer *lipgloss.Renderer
	User     string
	Version  string
	Session  context.Context
	Files    *uploads.Store
	// Downloads receives exported changes. Nil disables export.
	Downloads *downloads.Store
	// Edits holds the per-user edited versions. Nil disables edit mode.
	Edits *edits.Manager
	GPU   *gpuinfo.Poller
	// Sys samples host CPU and RAM for the panel. Nil hides them.
	Sys      *sysstat.Poller
	Unloader SoleUnloader
	Budget   int
	// Resolver reads model headers for the fit check. Nil disables it.
	Resolver *fit.Resolver
	// Learned holds measured usage, the fallback when headers are unknown.
	Learned *fit.Learned
	// ReadRAM reads system memory for the CPU target.
	ReadRAM func() (sysmem.Info, error)
	// Hub searches Docker Hub and Pulls runs the shared download queue.
	// Either nil hides the search.
	Hub   *hub.Client
	Pulls *pull.Manager
	// DiskFree reads the model disk; defaults to the filesystem holding
	// the configured models directory.
	DiskFree func() (diskspace.Info, error)
}

// Model is the bubbletea application for one SSH session.
type Model struct {
	cfg       config.Config
	client    *dmr.Client
	gate      *gpu.Gate
	theme     theme
	user      string
	version   string
	session   context.Context
	files     *uploads.Store
	downloads *downloads.Store
	edits     *edits.Manager
	protocol  edits.Protocol
	gpu       *gpuinfo.Poller
	sys       *sysstat.Poller
	unloader  SoleUnloader
	budget    int
	// baseBudget is CONTEXT_TOKEN_BUDGET, used until a context is chosen.
	baseBudget int
	resolver   *fit.Resolver
	learned    *fit.Learned
	readRAM    func() (sysmem.Info, error)
	hubc       *hub.Client
	pulls      *pull.Manager
	diskFree   func() (diskspace.Info, error)
	hub        *hubState
	fit        *fitState
	ctxSize    int
	target     fit.Target

	screen   screen
	width    int
	height   int
	loadKind loadKind

	list          list.Model
	spinner       spinner.Model
	viewport      viewport.Model
	input         textinput.Model
	models        []dmr.Model
	modelID       string
	turns         []turn
	partial       string
	partialR      string
	think         think.Parser
	thinkStart    time.Time
	thinkStop     time.Time
	liveOpen      bool
	streaming     bool
	markdown      bool
	fetching      bool
	fileConfirm   bool
	fileSel       string
	fileTop       int
	fileRight     bool
	previewTop    int
	paneRows      int
	noVision      map[string]bool
	skipImages    bool
	imageRetried  bool
	pendingImages []string
	mdCache       map[string]mdEntry
	mdByWidth     map[int]*glamour.TermRenderer
	status        string
	errText       string

	// Context gauge and the ctrl+x panel.
	ctxDetected  int
	usage        *dmr.Usage
	pendingUsage *dmr.Usage
	usageTurns   int
	summary      string
	compressing  bool
	ctxConfirm   bool
	ctxNote      string

	// Edit review state.
	editOn           bool
	seen             map[string]bool
	chPath           string
	chFocus          bool
	chFileView       bool
	chAgainstCurrent bool
	chTop            int
	chListTop        int
	chNote           string
	exporting        bool
	exported         bool
	archive          string

	gen    int
	cancel context.CancelFunc
}

type modelsMsg struct {
	gen    int
	models []dmr.Model
	err    error
}

type warmMsg struct {
	gen   int
	model string
	err   error
}

type unloadMsg struct {
	gen int
	n   int
	err error
}

type streamMsg struct {
	gen    int
	ev     dmr.StreamEvent
	ch     <-chan dmr.StreamEvent
	closed bool
}

type sessionEndedMsg struct{}

type homeMsg struct {
	n       int
	skipped bool
	err     error
}

// New builds a session UI. The first frame uses 80x24 until a resize arrives.
func New(opts Options) *Model {
	if opts.Gate == nil {
		opts.Gate = gpu.New()
	}
	if strings.TrimSpace(opts.User) == "" {
		opts.User = "guest"
	}
	if strings.TrimSpace(opts.Version) == "" {
		opts.Version = "1.4.0"
	}
	if opts.Budget <= 0 {
		opts.Budget = uploads.DefaultBudget
	}
	if opts.Session == nil {
		opts.Session = context.Background()
	}
	th := newTheme(opts.Renderer)

	delegate := list.NewDefaultDelegate()
	items := list.New([]list.Item{}, delegate, 80, 20)
	items.SetShowTitle(false)
	items.SetShowHelp(false)
	items.SetShowFilter(true)
	items.SetShowStatusBar(true)
	items.SetShowPagination(true)
	items.SetFilteringEnabled(true)
	items.DisableQuitKeybindings()
	items.SetStatusBarItemName("model", "models")
	styleList(opts.Renderer, &items)

	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(th.spinner))

	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = opts.Config.Chat.Placeholder
	ti.PromptStyle = th.accent
	ti.TextStyle = th.text
	ti.PlaceholderStyle = th.muted
	ti.CharLimit = 0
	ti.Width = 72

	vp := viewport.New(80, 16)

	return &Model{
		cfg:       opts.Config,
		client:    opts.Client,
		gate:      opts.Gate,
		theme:     th,
		user:      opts.User,
		version:   opts.Version,
		session:   opts.Session,
		files:     opts.Files,
		downloads: opts.Downloads,
		edits:     opts.Edits,
		protocol:  edits.Blocks{},
		editOn:    opts.Config.EditOn(),
		gpu:       opts.GPU,
		sys:       opts.Sys,
		unloader:  opts.Unloader,
		budget:    opts.Budget,
		resolver:  opts.Resolver,
		learned:   opts.Learned,
		readRAM:   opts.ReadRAM,
		hubc:      opts.Hub,
		pulls:     opts.Pulls,
		diskFree:  opts.DiskFree,

		baseBudget: opts.Budget,
		markdown:   opts.Config.MarkdownOn(),
		screen:     screenWelcome,
		width:      80,
		height:     24,
		list:       items,
		spinner:    sp,
		viewport:   vp,
		input:      ti,
	}
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	if m.session != nil && m.session.Done() != nil {
		cmds = append(cmds, waitSession(m.session))
	}
	return tea.Batch(cmds...)
}

func waitSession(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		<-ctx.Done()
		return sessionEndedMsg{}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		if m.screen == screenChat {
			m.syncTranscript(m.viewport.AtBottom())
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case sessionEndedMsg:
		return m.quit()
	case tea.KeyMsg:
		return m.onKey(msg)
	case modelsMsg:
		return m.onModels(msg)
	case warmMsg:
		return m.onWarm(msg)
	case unloadMsg:
		return m.onUnload(msg)
	case streamMsg:
		return m.onStream(msg)
	case homeMsg:
		return m.onHome(msg)
	case probeMsg:
		return m.onProbe(msg)
	case applyMsg:
		return m.onApply(msg)
	case freedMsg:
		return m.onFreed(msg)
	case exportMsg:
		return m.onExport(msg)
	case ctxSizeMsg:
		return m.onCtxSize(msg)
	case compressMsg:
		return m.onCompress(msg)
	case hubReposMsg:
		return m.onHubRepos(msg)
	case hubTagsMsg:
		return m.onHubTags(msg)
	case hubTickMsg:
		return m.onHubTick()
	default:
		return m, nil
	}
}

func (m *Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+q":
		return m.quit()
	case "ctrl+c":
		if m.screen == screenChat {
			return m.cancelGeneration()
		}
		return m, nil
	}
	switch m.screen {
	case screenWelcome:
		// Esc is not an exit key. Any other key opens the model list.
		if msg.String() == "esc" {
			return m, nil
		}
		return m.openModels()
	case screenLoading:
		return m, nil
	case screenModels:
		return m.onModelsKey(msg)
	case screenChat:
		return m.onChatKey(msg)
	case screenContext:
		return m.onContextKey(msg)
	case screenTarget:
		return m.onTargetKey(msg)
	case screenFit:
		return m.onFitKey(msg)
	case screenChanges:
		return m.onChangesKey(msg)
	case screenCtxInfo:
		return m.onCtxPanelKey(msg)
	case screenFiles:
		return m.onFilesKey(msg)
	case screenHub:
		return m.onHubKey(msg)
	default:
		return m, nil
	}
}

func (m *Model) onModelsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	filtering := len(m.models) > 0 && !m.fetching && m.list.FilterState() == list.Filtering
	if filtering && msg.String() != "esc" {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc":
		if filtering || m.list.FilterState() == list.FilterApplied {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		return m, nil
	case "r":
		if filtering {
			break
		}
		return m, m.startFetch()
	case "p":
		if !filtering && m.hubReady() {
			return m.openHub()
		}
	case "enter":
		if m.fetching || len(m.models) == 0 {
			return m, nil
		}
		return m.selectModel()
	}
	if m.fetching || len(m.models) == 0 {
		return m, nil
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *Model) onChatKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.backToWelcome()
	case "ctrl+n":
		return m.changeModel()
	case "ctrl+o":
		if m.streaming || m.modelID == "" || !m.cfg.ConfiguresDMR() {
			return m, nil
		}
		m.commitPartial()
		return m.startProbe(m.modelID, true)
	case "ctrl+t":
		m.toggleThinking()
		return m, nil
	case "ctrl+e":
		return m.openChanges()
	case "ctrl+x":
		return m.openCtxPanel()
	case "ctrl+r":
		m.markdown = !m.markdown
		m.syncTranscript(m.viewport.AtBottom())
		return m, nil
	case "ctrl+f":
		return m.openFiles()
	case "pgup", "ctrl+u":
		m.viewport.ScrollUp(scrollStep(m.viewport.Height))
		return m, nil
	case "pgdown", "ctrl+d":
		m.viewport.ScrollDown(scrollStep(m.viewport.Height))
		return m, nil
	case "enter":
		if m.streaming {
			return m, nil
		}
		return m.submit()
	default:
		return m.passInput(msg)
	}
}

func (m *Model) passInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.streaming {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func scrollStep(height int) int {
	n := height / 2
	if n < 1 {
		return 1
	}
	return n
}

func (m *Model) onModels(msg modelsMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.fetching = false
	if msg.err != nil {
		m.models = nil
		m.errText = oneLine(msg.err)
		_ = m.list.SetItems(nil)
		return m, nil
	}
	m.errText = ""
	m.models = msg.models
	return m, m.list.SetItems(modelItems(msg.models))
}

func (m *Model) onWarm(msg warmMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	if msg.err != nil {
		m.screen = screenModels
		m.errText = oneLine(msg.err)
		m.status = ""
		return m, nil
	}
	shown := (dmr.Model{ID: msg.model}).DisplayName()
	m.ctxSize = 0
	m.budget = m.baseBudget
	return m.enterChat(msg.model, "Using "+shown+". This GPU is shared with every SSH session.")
}

func (m *Model) enterChat(model, note string) (tea.Model, tea.Cmd) {
	m.fit = nil
	m.errText = ""
	m.status = ""
	m.screen = screenChat
	if model != m.modelID {
		m.usage = nil
		m.pendingUsage = nil
	}
	m.modelID = model
	m.ctxDetected = 0
	m.turns = append(m.turns, turn{role: roleNote, content: note})
	m.syncTranscript(true)
	if m.ctxSize == 0 {
		return m, tea.Batch(m.input.Focus(), m.fetchCtxSize(model))
	}
	return m, m.input.Focus()
}

func (m *Model) onUnload(msg unloadMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	m.screen = screenModels
	m.modelID = ""
	if msg.err != nil {
		m.errText = oneLine(msg.err)
		m.status = ""
	} else {
		m.errText = ""
		m.status = "Unloaded " + plural(msg.n, "runner", "runners") + ". A runner busy in another session stays loaded until that reply finishes."
	}
	return m, m.startFetch()
}

func (m *Model) onStream(msg streamMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	if msg.closed || msg.ev.Done {
		m.finishStream(nil)
		return m, nil
	}
	if msg.ev.Err != nil {
		if m.retryWithoutImages(msg.ev.Err) {
			return m, m.restartStream()
		}
		m.finishStream(msg.ev.Err)
		return m, nil
	}
	m.absorbUsage(msg.ev.Usage)
	m.absorb(msg.ev.Content, msg.ev.Reasoning)
	m.syncTranscript(m.viewport.AtBottom())
	return m, waitStream(msg.gen, msg.ch)
}

func (m *Model) absorb(content, reasoning string) {
	visible, hidden := m.think.Push(content)
	m.partial += visible
	// Reasoning-channel tokens are already split by the model. Concatenate
	// them. Think-tag blocks insert their own separators.
	m.partialR += reasoning
	m.partialR += hidden
	m.noteReasoning()
}

func (m *Model) noteReasoning() {
	active := m.think.InThink() || (strings.TrimSpace(m.partialR) != "" && strings.TrimSpace(m.partial) == "")
	if active && m.thinkStart.IsZero() {
		m.thinkStart = time.Now()
	}
	if !active && !m.thinkStart.IsZero() && m.thinkStop.IsZero() {
		m.thinkStop = time.Now()
	}
}

func (m *Model) thinkingNow() bool {
	if !m.streaming {
		return false
	}
	if m.think.InThink() {
		return true
	}
	return strings.TrimSpace(m.partialR) != "" && strings.TrimSpace(m.partial) == ""
}

func (m *Model) liveElapsed() time.Duration {
	if m.thinkStart.IsZero() {
		return 0
	}
	end := m.thinkStop
	if end.IsZero() || m.thinkingNow() {
		end = time.Now()
	}
	return end.Sub(m.thinkStart)
}

func (m *Model) toggleThinking() {
	if m.streaming && (strings.TrimSpace(m.partialR) != "" || m.think.InThink()) {
		m.liveOpen = !m.liveOpen
		m.syncTranscript(m.viewport.AtBottom())
		return
	}
	for i := len(m.turns) - 1; i >= 0; i-- {
		if m.turns[i].role == roleAssistant && strings.TrimSpace(m.turns[i].reasoning) != "" {
			m.turns[i].reasoningOpen = !m.turns[i].reasoningOpen
			m.syncTranscript(m.viewport.AtBottom())
			return
		}
	}
}

func (m *Model) openModels() (tea.Model, tea.Cmd) {
	m.screen = screenModels
	m.errText = ""
	return m, m.startFetch()
}

func (m *Model) selectModel() (tea.Model, tea.Cmd) {
	item, ok := m.list.SelectedItem().(modelItem)
	if !ok || item.model.ID == "" {
		return m, nil
	}
	if m.cfg.ConfiguresDMR() {
		return m.startProbe(item.model.ID, false)
	}
	return m.startWarm(item.model.ID)
}

func (m *Model) startWarm(id string) (tea.Model, tea.Cmd) {
	m.loadKind = loadWarm
	m.screen = screenLoading
	m.errText = ""
	m.status = fillModel(m.cfg.Loading.Message, (dmr.Model{ID: id}).DisplayName())
	ctx, gen := m.begin()
	if m.client == nil {
		return m, func() tea.Msg {
			return warmMsg{gen: gen, model: id, err: errors.New("DMR client is not configured")}
		}
	}
	client := m.client
	gate := m.gate
	return m, func() tea.Msg {
		err := gate.Do(ctx, func(ctx context.Context) error {
			return client.Warm(ctx, id)
		})
		return warmMsg{gen: gen, model: id, err: err}
	}
}

func (m *Model) changeModel() (tea.Model, tea.Cmd) {
	m.commitPartial()
	m.loadKind = loadUnload
	m.screen = screenLoading
	m.errText = ""
	m.status = "Unloading models to free the shared GPU…"
	ctx, gen := m.begin()
	if m.client == nil {
		return m, func() tea.Msg {
			return unloadMsg{gen: gen, err: errors.New("DMR client is not configured")}
		}
	}
	client := m.client
	gate := m.gate
	return m, func() tea.Msg {
		var n int
		err := gate.Do(ctx, func(ctx context.Context) error {
			var uerr error
			n, uerr = client.UnloadAll(ctx)
			return uerr
		})
		return unloadMsg{gen: gen, n: n, err: err}
	}
}

func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.modelID == "" {
		return m, nil
	}
	m.input.SetValue("")
	m.turns = append(m.turns, turn{role: roleUser, content: text})
	m.resetLiveReply()
	m.errText = ""
	m.skipImages = false
	m.imageRetried = false
	m.pendingImages = nil
	if !m.imagesAllowed() && m.hasPendingImages() {
		m.errText = visionNotice
	}
	m.status = "Generating…"
	ctx, gen := m.begin()
	m.streaming = true
	m.syncTranscript(true)
	if m.client == nil {
		return m, func() tea.Msg {
			return streamMsg{gen: gen, ev: dmr.StreamEvent{Err: errors.New("DMR client is not configured")}}
		}
	}
	ch := m.client.ChatStream(ctx, m.modelID, m.apiMessages())
	return m, waitStream(gen, ch)
}

func (m *Model) apiMessages() []dmr.Message {
	out := make([]dmr.Message, 0, len(m.turns)+1)
	sys := m.systemPrompt()
	if sum := strings.TrimSpace(m.summary); sum != "" {
		if sys != "" {
			sys += "\n\n"
		}
		sys += "Summary of the earlier conversation:\n" + sum
	}
	if sys != "" {
		out = append(out, dmr.Message{Role: "system", Content: sys})
	}
	if m.files != nil {
		var ov uploads.Overlay
		if m.edits != nil {
			ov = m.workspace()
		}
		res := m.files.ContextWith(m.user, m.budget, ov)
		m.seen = res.Full
		if text := res.Text; strings.TrimSpace(text) != "" {
			out = append(out, dmr.Message{
				Role:    "user",
				Content: "Files uploaded into this session. Use them as context.\n\n" + text,
			})
		}
	}
	lastUser := -1
	for _, item := range m.turns {
		switch item.role {
		case roleUser:
			out = append(out, dmr.Message{Role: "user", Content: item.content})
			lastUser = len(out) - 1
		case roleAssistant:
			if item.content != "" {
				out = append(out, dmr.Message{Role: "assistant", Content: item.content})
			}
		}
	}
	if lastUser >= 0 && m.files != nil && m.imagesAllowed() && !m.skipImages {
		imgs := m.files.PendingImages(m.user)
		if len(imgs) > 0 {
			parts := []dmr.ContentPart{{Type: "text", Text: out[lastUser].Content}}
			names := make([]string, 0, len(imgs))
			for _, img := range imgs {
				parts = append(parts, dmr.ImagePart(img.MIME, img.Data))
				names = append(names, img.Name)
			}
			out[lastUser].Parts = parts
			m.pendingImages = names
		}
	}
	return out
}

const visionNotice = "This model does not accept images (no vision/mmproj). Text was sent without images."

func (m *Model) imagesAllowed() bool {
	id := m.modelID
	disp := (dmr.Model{ID: id}).DisplayName()
	if matchGlobList(m.cfg.Uploads.VisionModels, id, disp) {
		return true
	}
	if matchGlobList(m.cfg.Uploads.NonVisionModels, id, disp) {
		return false
	}
	if m.noVision[id] {
		return false
	}
	return true
}

func (m *Model) hasPendingImages() bool {
	if m.files == nil {
		return false
	}
	return len(m.files.PendingImages(m.user)) > 0
}

func (m *Model) retryWithoutImages(err error) bool {
	if m.imageRetried || len(m.pendingImages) == 0 || !dmr.VisionRejected(err) {
		return false
	}
	if m.noVision == nil {
		m.noVision = map[string]bool{}
	}
	m.noVision[m.modelID] = true
	m.imageRetried = true
	m.skipImages = true
	m.pendingImages = nil
	m.partial = ""
	m.partialR = ""
	m.think = think.Parser{}
	m.liveOpen = false
	m.errText = visionNotice
	m.streaming = true
	m.status = "Generating…"
	return true
}

func (m *Model) restartStream() tea.Cmd {
	if m.client == nil {
		gen := m.gen
		return func() tea.Msg {
			return streamMsg{gen: gen, ev: dmr.StreamEvent{Err: errors.New("DMR client is not configured")}}
		}
	}
	if m.cancel != nil {
		m.cancel()
	}
	parent := m.session
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	ch := m.client.ChatStream(ctx, m.modelID, m.apiMessages())
	return waitStream(m.gen, ch)
}

func (m *Model) cancelGeneration() (tea.Model, tea.Cmd) {
	if !m.streaming {
		return m, nil
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.gen++
	m.finishStream(context.Canceled)
	m.status = "Generation cancelled"
	return m, nil
}

func (m *Model) backToWelcome() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.gen++
	if m.streaming {
		m.finishStream(context.Canceled)
	}
	m.screen = screenLoading
	m.loadKind = loadUnload
	m.status = "Unloading models…"
	unloader := m.unloader
	return m, func() tea.Msg {
		if unloader == nil {
			return homeMsg{skipped: true}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		n, skipped, err := unloader.UnloadIfSole(ctx)
		return homeMsg{n: n, skipped: skipped, err: err}
	}
}

func (m *Model) onHome(msg homeMsg) (tea.Model, tea.Cmd) {
	m.screen = screenWelcome
	m.modelID = ""
	m.turns = nil
	m.resetLiveReply()
	m.errText = ""
	switch {
	case msg.err != nil:
		m.errText = oneLine(msg.err)
		m.status = ""
	case msg.skipped:
		m.status = "Other sessions are connected. Models were left loaded."
	default:
		m.status = "Unloaded " + plural(msg.n, "runner", "runners") + "."
	}
	return m, nil
}

func (m *Model) revealFile(index, n int) {
	rows := m.filesBoxRows()
	if index < m.fileTop {
		m.fileTop = index
	}
	if index >= m.fileTop+rows {
		m.fileTop = index - rows + 1
	}
	if m.fileTop < 0 {
		m.fileTop = 0
	}
	if n > rows && m.fileTop > n-rows {
		m.fileTop = n - rows
	}
}

func (m *Model) filesBoxRows() int {
	if m.screen == screenFiles && m.paneRows > 0 {
		return m.paneRows
	}
	n := m.cfg.Uploads.FilesBoxRows
	if n < 1 {
		return 4
	}
	return n
}

func waitStream(gen int, ch <-chan dmr.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamMsg{gen: gen, closed: true}
		}
		return streamMsg{gen: gen, ev: ev, ch: ch}
	}
}

func (m *Model) finishStream(err error) {
	m.streaming = false
	m.compressing = false
	m.commitPartial()
	if err == nil {
		m.commitUsage()
	} else {
		m.pendingUsage = nil
	}
	m.status = ""
	if err != nil && !errors.Is(err, context.Canceled) {
		m.errText = oneLine(err)
		if dmr.ContextError(err) {
			m.errText = "The conversation no longer fits the context. ctrl+x to compress or reset, or ctrl+o for a larger context. " + m.errText
		}
	} else if err == nil && m.files != nil && len(m.pendingImages) > 0 {
		m.files.MarkSent(m.user, m.pendingImages)
		m.pendingImages = nil
	}
	if err == nil {
		m.proposeEdits()
	}
	m.syncTranscript(true)
}

func (m *Model) commitPartial() {
	c, r := m.think.Flush()
	m.partial += c
	m.partialR += r
	m.noteReasoning()
	if strings.TrimSpace(m.partial) == "" && strings.TrimSpace(m.partialR) == "" {
		m.resetLiveReply()
		m.streaming = false
		return
	}
	if !m.thinkStart.IsZero() && m.thinkStop.IsZero() {
		m.thinkStop = time.Now()
	}
	var dur time.Duration
	if !m.thinkStart.IsZero() {
		dur = m.thinkStop.Sub(m.thinkStart)
		if dur < 0 {
			dur = 0
		}
	}
	m.turns = append(m.turns, turn{
		role:          roleAssistant,
		content:       m.partial,
		reasoning:     strings.TrimSpace(m.partialR),
		reasoningOpen: m.liveOpen,
		thinkFor:      dur,
	})
	m.resetLiveReply()
	m.streaming = false
}

func (m *Model) resetLiveReply() {
	m.partial = ""
	m.partialR = ""
	m.think = think.Parser{}
	m.thinkStart = time.Time{}
	m.thinkStop = time.Time{}
	m.liveOpen = false
}

func (m *Model) startFetch() tea.Cmd {
	ctx, gen := m.begin()
	m.fetching = true
	if m.status == "" {
		m.status = m.cfg.Models.Loading
	}
	if m.client == nil {
		return func() tea.Msg {
			return modelsMsg{gen: gen, err: errors.New("DMR client is not configured")}
		}
	}
	client := m.client
	return func() tea.Msg {
		models, err := client.ListModels(ctx)
		return modelsMsg{gen: gen, models: models, err: err}
	}
}

func (m *Model) begin() (context.Context, int) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.gen++
	m.streaming = false
	parent := m.session
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	return ctx, m.gen
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	return m, func() tea.Msg { return tea.Quit() }
}

func (m *Model) compact() bool {
	return m.width < 48 || m.height < 16
}

func (m *Model) syncTranscript(stick bool) {
	atBottom := stick || m.viewport.AtBottom()
	m.viewport.SetContent(m.transcript())
	if atBottom {
		m.viewport.GotoBottom()
	}
}
