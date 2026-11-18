package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/fit"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/UnixEU/ssh-llm-tui/internal/sysmem"
	tea "github.com/charmbracelet/bubbletea"
)

const mibBytes = int64(1) << 20

// fitState is the model setup flow: probe → context → target → check → load.
type fitState struct {
	model   string
	info    fit.ModelInfo
	infoErr error
	gpus    []fit.Device
	ram     fit.Device
	cpus    int
	// usedMiB is memory.used per GPU at probe time, for learned usage.
	usedMiB map[int]int
	// loaded is true when DMR already runs this model; busy when that
	// runner is serving a request.
	loaded  bool
	busy    bool
	current *dmr.ModelConfig
	// others counts runners of other models.
	others int

	choices []int
	ctxIdx  int
	targets []fit.Target
	tgtIdx  int

	result     fit.Result
	sameConfig bool
	suggestCtx int
	confirm    bool
	fromChat   bool
	errText    string
}

func (f *fitState) ctx() int {
	if f == nil || len(f.choices) == 0 {
		return 0
	}
	return f.choices[clampIndex(f.ctxIdx, len(f.choices))]
}

func (f *fitState) target() fit.Target {
	if f == nil || len(f.targets) == 0 {
		return fit.Target{Kind: fit.TargetAuto}
	}
	return f.targets[clampIndex(f.tgtIdx, len(f.targets))]
}

func clampIndex(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

type probeMsg struct {
	gen     int
	model   string
	info    fit.ModelInfo
	infoErr error
	snap    gpuinfo.Snapshot
	ram     sysmem.Info
	ramErr  error
	runners []dmr.Runner
	configs []dmr.ModelConfig
	err     error
}

// startProbe gathers what the pickers and the fit check need: model
// metadata, a fresh GPU sample, system memory, and DMR's runner state.
func (m *Model) startProbe(id string, fromChat bool) (tea.Model, tea.Cmd) {
	m.loadKind = loadProbe
	m.screen = screenLoading
	m.errText = ""
	m.status = "Checking memory for " + (dmr.Model{ID: id}).DisplayName() + "…"
	m.fit = &fitState{model: id, fromChat: fromChat}
	ctx, gen := m.begin()
	client, gpus, resolver, readRAM := m.client, m.gpu, m.resolver, m.readRAM
	return m, func() tea.Msg {
		msg := probeMsg{gen: gen, model: id}
		msg.info, msg.infoErr = resolver.Info(id)
		if gpus != nil {
			msg.snap = gpus.Refresh(ctx)
		}
		if readRAM != nil {
			msg.ram, msg.ramErr = readRAM()
		} else {
			msg.ramErr = errors.New("system memory not available")
		}
		if client != nil {
			// Older DMR builds may lack these routes; the check still runs.
			msg.runners, _ = client.Running(ctx)
			msg.configs, _ = client.Configs(ctx, id)
		}
		return msg
	}
}

func (m *Model) onProbe(msg probeMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen || m.fit == nil {
		return m, nil
	}
	f := m.fit
	f.info, f.infoErr = msg.info, msg.infoErr
	f.usedMiB = map[int]int{}
	f.gpus = nil
	if msg.snap.OK {
		for _, g := range msg.snap.GPUs {
			f.gpus = append(f.gpus, fit.Device{
				Index:      g.Index,
				Name:       g.Name,
				FreeBytes:  int64(g.MemFreeMiB) * mibBytes,
				TotalBytes: int64(g.MemTotalMiB) * mibBytes,
			})
			f.usedMiB[g.Index] = g.MemUsedMiB
		}
	}
	if msg.ramErr == nil {
		f.ram = fit.Device{Index: -1, Name: "system RAM", FreeBytes: msg.ram.AvailableBytes, TotalBytes: msg.ram.TotalBytes}
		f.cpus = msg.ram.CPUs
	}
	for _, r := range msg.runners {
		if dmr.SameModel(r.Model, msg.model) {
			f.loaded = true
			f.busy = f.busy || r.InUse
		} else {
			f.others++
		}
	}
	for i := range msg.configs {
		if dmr.SameModel(msg.configs[i].Model, msg.model) {
			c := msg.configs[i]
			f.current = &c
		}
	}

	f.choices = fit.CapContexts(m.cfg.Fit.ContextChoices, f.info.MaxContext())
	f.targets = []fit.Target{{Kind: fit.TargetAuto}}
	if len(f.gpus) > 1 {
		for _, g := range f.gpus {
			f.targets = append(f.targets, fit.Target{Kind: fit.TargetGPU, GPU: g.Index})
		}
	}
	if m.cfg.CPUAllowed() {
		f.targets = append(f.targets, fit.Target{Kind: fit.TargetCPU})
	}
	f.tgtIdx = m.initialTarget()
	f.ctxIdx = m.initialContext()

	m.status = ""
	switch {
	case m.cfg.ContextPicker():
		m.screen = screenContext
	case m.cfg.TargetPicker() && len(f.targets) > 1:
		m.screen = screenTarget
	default:
		return m.evaluate()
	}
	return m, nil
}

// initialTarget keeps the target the model is configured with, else Auto.
func (m *Model) initialTarget() int {
	f := m.fit
	if f.current != nil {
		for i, t := range f.targets {
			if sameFlags(t.RuntimeFlags(), targetFlags(f.current.RuntimeFlags)) {
				return i
			}
		}
	}
	return 0
}

// initialContext prefers the model's current setting while it is loaded,
// then DEFAULT_CONTEXT, then the largest choice that fits.
func (m *Model) initialContext() int {
	f := m.fit
	if f.loaded && f.current != nil && f.current.ContextSize > 0 {
		for i, c := range f.choices {
			if c == f.current.ContextSize {
				return i
			}
		}
	}
	if want := m.cfg.Fit.DefaultContextTokens; want > 0 {
		best := 0
		for i, c := range f.choices {
			if c <= want {
				best = i
			}
		}
		return best
	}
	best := 0
	for i := range f.choices {
		v := m.checkAt(f.choices[i], f.target()).Verdict
		if v == fit.Fits {
			best = i
		}
	}
	return best
}

func (m *Model) margins() fit.Margins {
	return fit.Margins{MinBytes: m.cfg.Fit.MarginMiB * mibBytes, Percent: m.cfg.Fit.MarginPct}
}

// estimateAt returns the estimate for a context and target, and false when
// there is nothing to base it on.
func (m *Model) estimateAt(ctx int, t fit.Target) (fit.Estimate, bool) {
	f := m.fit
	if f.info.Known() {
		return fit.EstimateFor(f.info, ctx, m.cfg.Fit.KVCacheType), true
	}
	if u, ok := m.learned.Get(f.model, ctx, t); ok {
		return fit.EstimateFromUsage(u, ctx), true
	}
	return fit.Estimate{}, false
}

func (m *Model) checkAt(ctx int, t fit.Target) fit.Result {
	est, ok := m.estimateAt(ctx, t)
	if !ok {
		return fit.Result{Target: t, Verdict: fit.Unknown, Reason: m.unknownReason()}
	}
	return fit.Check(est, t, m.fit.gpus, m.fit.ram, m.margins())
}

func (m *Model) unknownReason() string {
	f := m.fit
	switch {
	case errors.Is(f.infoErr, fit.ErrNoStore):
		return "the model store is not mounted, so the model's size is unknown"
	case f.infoErr != nil:
		return "the model header could not be read: " + oneLine(f.infoErr)
	}
	return "the model's memory need is unknown"
}

// runtimeFlags are the llama.cpp flags for the current choice.
func (m *Model) runtimeFlags(t fit.Target) []string {
	flags := append([]string{}, t.RuntimeFlags()...)
	if t.Kind == fit.TargetCPU && m.cfg.Fit.CPUThreads > 0 {
		flags = append(flags, "--threads", strconv.Itoa(m.cfg.Fit.CPUThreads))
	}
	if kv := m.cfg.Fit.KVCacheType; kv != "" {
		flags = append(flags, "--cache-type-k", kv, "--cache-type-v", kv)
	}
	return flags
}

func (m *Model) configureRequest() dmr.ConfigureRequest {
	f := m.fit
	req := dmr.ConfigureRequest{Model: f.model, RuntimeFlags: m.runtimeFlags(f.target())}
	if c := f.ctx(); c > 0 {
		n := int32(c)
		req.ContextSize = &n
	}
	return req
}

// evaluate runs the fit check for the current choice and shows the result.
func (m *Model) evaluate() (tea.Model, tea.Cmd) {
	f := m.fit
	f.confirm = false
	f.errText = ""
	ctx, t := f.ctx(), f.target()
	f.sameConfig = f.loaded && f.current != nil && f.current.ContextSize == ctx &&
		sameFlags(f.current.RuntimeFlags, m.runtimeFlags(t))
	f.result = m.checkAt(ctx, t)
	f.suggestCtx = 0
	if f.result.Verdict == fit.NoFit || f.result.Verdict == fit.Tight {
		if best, ok := m.largestFitting(t); ok && best != ctx {
			f.suggestCtx = best
		}
	}
	if !m.cfg.FitOn() {
		return m.applyFit()
	}
	m.screen = screenFit
	return m, nil
}

func (m *Model) largestFitting(t fit.Target) (int, bool) {
	best, ok := 0, false
	for _, c := range m.fit.choices {
		if m.checkAt(c, t).Verdict == fit.Fits && c > best {
			best, ok = c, true
		}
	}
	return best, ok
}

// canLoad reports whether Enter loads, and why not.
func (m *Model) canLoad() (bool, string) {
	f := m.fit
	if f.sameConfig {
		return true, ""
	}
	switch f.result.Verdict {
	case fit.Fits, fit.Tight:
		return true, ""
	case fit.NoFit:
		return false, "Not enough free memory for this choice."
	}
	if m.cfg.FitStrict() {
		return false, "FIT_CHECK=strict refuses models whose memory need is unknown."
	}
	if !f.confirm {
		return false, "Press y to load without a memory estimate."
	}
	return true, ""
}

type applyMsg struct {
	gen     int
	model   string
	ctx     int
	target  fit.Target
	cli     string
	learned bool
	err     error
}

// applyFit configures DMR for the choice and warms the model. It is only
// reached after the check allowed it (or FIT_CHECK=off).
func (m *Model) applyFit() (tea.Model, tea.Cmd) {
	f := m.fit
	req := m.configureRequest()
	t := f.target()
	skipConfigure := f.sameConfig
	m.loadKind = loadWarm
	m.screen = screenLoading
	m.errText = ""
	m.status = fmt.Sprintf("Loading %s · ctx %s · %s…", (dmr.Model{ID: f.model}).DisplayName(), ctxLabel(f.ctx()), t.Label())
	ctx, gen := m.begin()
	client, gate, gpus, learned := m.client, m.gate, m.gpu, m.learned
	before := f.usedMiB
	model := f.model
	ctxSize := f.ctx()
	record := !f.loaded && t.Kind != fit.TargetCPU
	return m, func() tea.Msg {
		out := applyMsg{gen: gen, model: model, ctx: ctxSize, target: t, cli: req.CLI()}
		if client == nil {
			out.err = errors.New("DMR client is not configured")
			return out
		}
		out.err = gate.Do(ctx, func(ctx context.Context) error {
			if !skipConfigure {
				if err := client.Configure(ctx, req); err != nil {
					return err
				}
			}
			return client.Warm(ctx, model)
		})
		if out.err == nil && record && gpus != nil && len(before) > 0 {
			after := gpus.Refresh(ctx)
			delta := map[int]int64{}
			for _, g := range after.GPUs {
				if prev, ok := before[g.Index]; ok {
					delta[g.Index] = int64(g.MemUsedMiB-prev) * mibBytes
				}
			}
			out.learned, _ = learned.Record(model, ctxSize, t, delta)
		}
		return out
	}
}

func (m *Model) onApply(msg applyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	if msg.err != nil {
		if m.fit != nil && m.cfg.FitOn() {
			m.screen = screenFit
			m.fit.errText = applyError(msg.err, m.fit)
		} else {
			m.screen = screenModels
			m.errText = applyError(msg.err, m.fit)
		}
		m.status = ""
		return m, nil
	}
	m.ctxSize = msg.ctx
	m.target = msg.target
	m.budget = budgetFor(msg.ctx, m.baseBudget)
	note := fmt.Sprintf("Using %s · ctx %s · %s. This GPU is shared with every SSH session.",
		(dmr.Model{ID: msg.model}).DisplayName(), ctxLabel(msg.ctx), msg.target.Label())
	return m.enterChat(msg.model, note)
}

func applyError(err error, f *fitState) string {
	if errors.Is(err, dmr.ErrRunnerBusy) {
		cur := ""
		if f != nil && f.current != nil && f.current.ContextSize > 0 {
			cur = " It is running with ctx " + ctxLabel(f.current.ContextSize) + "."
		}
		return "Another session is using this model right now, so its settings cannot change." + cur +
			" Wait for that reply to finish, or pick the settings it already uses."
	}
	return "Loading failed: " + oneLine(err) + " Try a smaller context, another target, or a smaller model."
}

// budgetFor sizes the file context budget from the model context: three
// quarters of it, leaving room for the conversation and the reply.
func budgetFor(ctx, fallback int) int {
	if ctx <= 0 {
		return fallback
	}
	b := ctx * 3 / 4
	if b < 1024 {
		b = 1024
	}
	return b
}

func ctxLabel(n int) string {
	if n <= 0 {
		return "default"
	}
	return fit.FormatTokens(n)
}

// targetFlags keeps the placement flags from a configured flag list.
func targetFlags(flags []string) []string {
	for i := 0; i+1 < len(flags); i++ {
		if flags[i] == "--device" || flags[i] == "-dev" {
			return []string{"--device", flags[i+1]}
		}
	}
	return nil
}

func sameFlags(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// Key handling for the three setup screens.

func (m *Model) onContextKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.fit
	switch msg.String() {
	case "up", "k":
		f.ctxIdx = clampIndex(f.ctxIdx-1, len(f.choices))
	case "down", "j":
		f.ctxIdx = clampIndex(f.ctxIdx+1, len(f.choices))
	case "enter":
		if m.cfg.TargetPicker() && len(f.targets) > 1 {
			m.screen = screenTarget
			return m, nil
		}
		return m.evaluate()
	case "esc":
		return m.leaveSetup()
	}
	return m, nil
}

func (m *Model) onTargetKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.fit
	switch msg.String() {
	case "up", "k":
		f.tgtIdx = clampIndex(f.tgtIdx-1, len(f.targets))
	case "down", "j":
		f.tgtIdx = clampIndex(f.tgtIdx+1, len(f.targets))
	case "enter":
		return m.evaluate()
	case "esc":
		if m.cfg.ContextPicker() {
			m.screen = screenContext
			return m, nil
		}
		return m.leaveSetup()
	}
	return m, nil
}

func (m *Model) onFitKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.fit
	switch msg.String() {
	case "enter":
		if ok, _ := m.canLoad(); ok {
			return m.applyFit()
		}
	case "y", "Y":
		if f.result.Verdict == fit.Unknown && !m.cfg.FitStrict() {
			f.confirm = true
			return m.applyFit()
		}
	case "s":
		if f.suggestCtx > 0 {
			for i, c := range f.choices {
				if c == f.suggestCtx {
					f.ctxIdx = i
				}
			}
			return m.evaluate()
		}
	case "p":
		for i, t := range f.targets {
			if t.Kind == fit.TargetCPU && i != f.tgtIdx {
				f.tgtIdx = i
				return m.evaluate()
			}
		}
	case "u":
		return m.freeAndRecheck()
	case "esc":
		switch {
		case m.cfg.TargetPicker() && len(f.targets) > 1:
			m.screen = screenTarget
		case m.cfg.ContextPicker():
			m.screen = screenContext
		default:
			return m.leaveSetup()
		}
	case "m":
		return m.leaveSetup()
	}
	return m, nil
}

// leaveSetup returns to the chat when the flow started there, else to the
// model list.
func (m *Model) leaveSetup() (tea.Model, tea.Cmd) {
	if m.fit != nil && m.fit.fromChat && m.modelID != "" {
		m.screen = screenChat
		m.fit = nil
		m.syncTranscript(true)
		return m, m.input.Focus()
	}
	m.fit = nil
	m.screen = screenModels
	return m, nil
}

type freedMsg struct {
	gen   int
	model string
	n     int
	err   error
}

// freeAndRecheck unloads idle runners (DMR keeps busy ones) and probes again.
func (m *Model) freeAndRecheck() (tea.Model, tea.Cmd) {
	model := m.fit.model
	fromChat := m.fit.fromChat
	m.loadKind = loadUnload
	m.screen = screenLoading
	m.status = "Unloading idle models…"
	ctx, gen := m.begin()
	client, gate := m.client, m.gate
	m.fit.fromChat = fromChat
	return m, func() tea.Msg {
		if client == nil {
			return freedMsg{gen: gen, model: model, err: errors.New("DMR client is not configured")}
		}
		var n int
		err := gate.Do(ctx, func(ctx context.Context) error {
			var uerr error
			n, uerr = client.UnloadAll(ctx)
			return uerr
		})
		return freedMsg{gen: gen, model: model, n: n, err: err}
	}
}

func (m *Model) onFreed(msg freedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	fromChat := m.fit != nil && m.fit.fromChat
	if msg.err != nil {
		m.screen = screenFit
		if m.fit != nil {
			m.fit.errText = "Unload failed: " + oneLine(msg.err)
		}
		return m, nil
	}
	if fromChat {
		// The model this chat used may be gone now.
		m.modelID = ""
	}
	return m.startProbe(msg.model, false)
}
