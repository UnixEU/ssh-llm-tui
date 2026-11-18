package ui

import (
	"fmt"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/fit"
)

// verdictMark is the symbol and colour for a fit verdict.
func (m *Model) verdictMark(v fit.Verdict) string {
	r := m.theme.r
	switch v {
	case fit.Fits:
		return r.NewStyle().Foreground(gpuGreen).Render("✓ fits")
	case fit.Tight:
		return r.NewStyle().Foreground(gpuYellow).Render("⚠ tight")
	case fit.NoFit:
		return r.NewStyle().Foreground(gpuRed).Render("✗ does not fit")
	}
	return m.theme.muted.Render("? unknown")
}

func (m *Model) setupHeader(title string) string {
	return m.headerBlock(title, "v"+m.version)
}

// modelLine names the model and what is known about it.
func (m *Model) modelLine() string {
	f := m.fit
	name := (dmr.Model{ID: f.model}).DisplayName()
	detail := f.info.Summary()
	if !f.info.Known() {
		detail = "size unknown: " + m.unknownReason()
	}
	line := m.theme.subtitle.Render(name)
	if detail != "" {
		line += m.theme.muted.Render("  " + detail)
	}
	return line
}

func (m *Model) loadedNote() string {
	f := m.fit
	switch {
	case f.busy:
		return m.theme.fileNote.Render("This model is answering in another session. Its settings cannot change until that reply finishes.")
	}
	note := ""
	switch {
	case f.loaded && f.current != nil && f.current.ContextSize > 0:
		note = "Loaded now with ctx " + ctxLabel(f.current.ContextSize) + ". Other settings reload it."
	case f.loaded:
		note = "Loaded now with Docker Model Runner defaults. Other settings reload it."
	}
	if note != "" && f.reclaim > 0 {
		note += " Its ≈ " + fit.FormatBytes(f.reclaim) + " on the GPU count as free."
	}
	if note == "" {
		return ""
	}
	return m.theme.muted.Render(note)
}

// estimateCell is "≈ 19.8 GiB" or "?".
func (m *Model) estimateCell(r fit.Result) string {
	if r.Verdict == fit.Unknown && len(r.Shares) == 0 {
		return "?"
	}
	s := "≈ " + fit.FormatBytes(r.Need())
	if r.Estimate.Measured {
		s += " (measured)"
	}
	return s
}

func (m *Model) viewContext() string {
	f := m.fit
	t := f.target()
	rows := []string{}
	for i, c := range f.choices {
		res := m.checkAt(c, t)
		marker := "  "
		label := m.theme.text.Render(fmt.Sprintf("%-6s", ctxLabel(c)))
		if i == clampIndex(f.ctxIdx, len(f.choices)) {
			marker = m.theme.accent.Render("› ")
			label = m.theme.accent.Render(fmt.Sprintf("%-6s", ctxLabel(c)))
		}
		rows = append(rows, marker+label+"  "+m.theme.text.Render(fmt.Sprintf("%-22s", m.estimateCell(res)))+m.verdictMark(res.Verdict))
	}
	if len(rows) == 0 {
		rows = append(rows, m.theme.muted.Render("No context sizes configured."))
	}
	notes := []string{m.theme.muted.Render("Estimates for " + m.targetName(t) + ". " + m.freeSummary(t))}
	if f.info.MaxContext() == 0 {
		notes = append(notes, m.theme.muted.Render("The model's maximum context is unknown; all sizes are offered."))
	}
	if n := m.loadedNote(); n != "" {
		notes = append(notes, n)
	}
	return joinLines(m.setupHeader("Context size"), m.modelLine(), "", m.boxed(strings.Join(rows, "\n")), "", strings.Join(notes, "\n"))
}

func (m *Model) viewTarget() string {
	f := m.fit
	ctx := f.ctx()
	rows := []string{}
	freeWidth := 0
	for _, t := range f.targets {
		if w := len([]rune(m.freeSummary(t))); w > freeWidth {
			freeWidth = w
		}
	}
	for i, t := range f.targets {
		res := m.checkAt(ctx, t)
		marker := "  "
		name := fmt.Sprintf("%-18s", m.targetName(t))
		label := m.theme.text.Render(name)
		if i == clampIndex(f.tgtIdx, len(f.targets)) {
			marker = m.theme.accent.Render("› ")
			label = m.theme.accent.Render(name)
		}
		free := m.theme.muted.Render(fmt.Sprintf("%-*s", freeWidth, m.freeSummary(t)))
		rows = append(rows, marker+label+"  "+free+"  "+m.verdictMark(res.Verdict))
	}
	notes := []string{m.theme.muted.Render("Context " + ctxLabel(ctx) + ". CPU runs from system RAM and is much slower than a GPU.")}
	return joinLines(m.setupHeader("Where to run the model"), m.modelLine(), "", m.boxed(strings.Join(rows, "\n")), "", strings.Join(notes, "\n"))
}

func (m *Model) targetName(t fit.Target) string {
	if m.fit == nil {
		return t.Label()
	}
	if t.Kind == fit.TargetGPU {
		for _, g := range m.fit.gpus {
			if g.Index == t.GPU && g.Name != "" {
				return fmt.Sprintf("GPU %d %s", t.GPU, shortGPUName(g.Name))
			}
		}
	}
	if t.Kind == fit.TargetAuto && len(m.fit.gpus) > 1 {
		return fmt.Sprintf("Auto (%d GPUs)", len(m.fit.gpus))
	}
	if t.Kind == fit.TargetAuto && len(m.fit.gpus) == 1 {
		return "GPU"
	}
	return t.Label()
}

func shortGPUName(name string) string {
	name = strings.TrimPrefix(name, "NVIDIA ")
	name = strings.TrimSuffix(name, " Generation")
	return clipRunes(name, 18)
}

// freeSummary is the free memory relevant to a target.
func (m *Model) freeSummary(t fit.Target) string {
	f := m.fit
	switch t.Kind {
	case fit.TargetCPU:
		if f.ram.TotalBytes <= 0 {
			return "RAM unknown"
		}
		s := fit.FormatBytes(f.ram.FreeBytes) + " RAM available"
		if f.cpus > 0 {
			s += fmt.Sprintf(", %d CPUs", f.cpus)
		}
		return s
	case fit.TargetGPU:
		for _, g := range f.gpus {
			if g.Index == t.GPU {
				return fit.FormatBytes(g.FreeBytes) + " free"
			}
		}
		return "not visible"
	}
	if len(f.gpus) == 0 {
		return "GPU memory unknown"
	}
	parts := make([]string, 0, len(f.gpus))
	for _, g := range f.gpus {
		parts = append(parts, strings.TrimSuffix(fit.FormatBytes(g.FreeBytes), " GiB"))
	}
	return strings.Join(parts, " + ") + " GiB free"
}

func (m *Model) viewFit() string {
	f := m.fit
	res := f.result
	t := f.target()
	lines := []string{
		m.theme.text.Render(fmt.Sprintf("Context %s · %s", ctxLabel(f.ctx()), m.targetName(t))),
	}
	switch {
	case f.sameConfig:
		lines = append(lines, m.theme.ok.Render("✓ Already loaded with these settings. Enter continues without reloading."))
	case res.Verdict == fit.Unknown:
		lines = append(lines,
			m.theme.muted.Render("?")+m.theme.text.Render(" No memory estimate: "+nonEmpty(res.Reason, m.unknownReason())+"."))
		if m.cfg.FitStrict() {
			lines = append(lines, m.theme.err.Render("Loading is refused while the need is unknown (FIT_CHECK=strict)."))
		} else {
			lines = append(lines, m.theme.fileNote.Render("Loading may fail or slow the shared GPU. Press y to load anyway."))
		}
	default:
		e := res.Estimate
		head := fmt.Sprintf("Needs ≈ %s", fit.FormatBytes(res.Need()))
		if e.Measured {
			head += " (measured on an earlier load)"
		} else {
			weights := e.Weights
			if t.Kind == fit.TargetCPU {
				weights += e.HostWeights
			}
			parts := []string{"weights " + fit.FormatBytes(weights), "KV " + fit.FormatBytes(e.KV)}
			if e.State > 0 {
				parts = append(parts, "state "+fit.FormatBytes(e.State))
			}
			parts = append(parts, "compute "+fit.FormatBytes(e.Compute))
			if e.Projector > 0 {
				parts = append(parts, "vision "+fit.FormatBytes(e.Projector))
			}
			head += " (" + strings.Join(parts, " · ") + fmt.Sprintf(", +%d%% margin of error)", int((e.Factor-1)*100+0.5))
		}
		lines = append(lines, m.theme.text.Render(head))
		for _, s := range res.Shares {
			name := "RAM "
			if t.Kind != fit.TargetCPU {
				name = fmt.Sprintf("GPU%d", s.Device.Index)
			}
			lines = append(lines, fmt.Sprintf("  %s  %s free → needs %s + %s headroom  %s",
				m.theme.text.Render(name),
				m.theme.text.Render(fmt.Sprintf("%8s", fit.FormatBytes(s.Device.FreeBytes))),
				m.theme.text.Render(fit.FormatBytes(s.Need)),
				m.theme.muted.Render(fit.FormatBytes(s.Margin)),
				m.verdictMark(s.Verdict)))
		}
		switch res.Verdict {
		case fit.NoFit:
			lines = append(lines, "", m.theme.err.Render(fmt.Sprintf("✗ Not enough memory: needs ≈ %s, %s free. The model will not be loaded.",
				fit.FormatBytes(res.Need()), fit.FormatBytes(res.Free()))))
		case fit.Tight:
			lines = append(lines, "", m.theme.fileNote.Render("⚠ Fits, but leaves less headroom than configured. Other sessions may run out of memory."))
		default:
			lines = append(lines, "", m.theme.ok.Render("✓ Fits."))
		}
	}
	if fixes := m.fixes(); len(fixes) > 0 {
		lines = append(lines, "", m.theme.muted.Render("Options:"))
		lines = append(lines, fixes...)
	}
	if f.errText != "" {
		lines = append(lines, "", m.theme.err.Render(f.errText))
	}
	if n := m.loadedNote(); n != "" && !f.sameConfig && !(f.busy && f.errText != "") {
		lines = append(lines, "", n)
	}
	req := m.configureRequest()
	req.Model = (dmr.Model{ID: req.Model}).DisplayName()
	cli := req.CLI()
	footer := m.theme.muted.Width(max(1, m.width)).Render("Same as: " + cli)
	return joinLines(m.setupHeader("Memory check"), m.modelLine(), "", m.boxed(strings.Join(lines, "\n")), "", footer)
}

// fixes lists the suggestions offered on the check screen.
func (m *Model) fixes() []string {
	f := m.fit
	if f.sameConfig || f.result.Verdict == fit.Fits {
		return nil
	}
	var out []string
	key := func(k, text string) string { return "  " + m.theme.key.Render(k) + " " + m.theme.text.Render(text) }
	if f.suggestCtx > 0 {
		out = append(out, key("s", "use ctx "+ctxLabel(f.suggestCtx)+", the largest that fits"))
	}
	t := f.target()
	if t.Kind != fit.TargetCPU {
		for _, cand := range f.targets {
			if cand.Kind == fit.TargetCPU {
				if v := m.checkAt(f.ctx(), cand).Verdict; v == fit.Fits || v == fit.Tight {
					out = append(out, key("p", "run on CPU (fits in RAM, slower)"))
				}
			}
		}
	}
	if f.others > 0 || f.loaded {
		out = append(out, key("u", "unload idle models, then check again"))
	}
	if f.result.Verdict == fit.NoFit {
		out = append(out, key("m", "pick a smaller model"))
	}
	return out
}

func (m *Model) fitHelp(short bool) []string {
	switch m.screen {
	case screenContext, screenTarget:
		if short {
			return []string{"↑/↓", "enter", "esc", "ctrl+q"}
		}
		return []string{"↑/↓ choose", "enter next", "esc back", "ctrl+q exit"}
	}
	ok, _ := m.canLoad()
	labels := []string{}
	if ok {
		labels = append(labels, "enter load")
	}
	if m.fit != nil && m.fit.result.Verdict == fit.Unknown && !m.fit.sameConfig && !m.cfg.FitStrict() {
		labels = append(labels, "y load anyway")
	}
	labels = append(labels, "esc back", "m models", "ctrl+q exit")
	if short {
		for i, l := range labels {
			labels[i] = strings.Fields(l)[0]
		}
	}
	return labels
}

// targetShort is the chat header suffix for the placement.
func targetShort(t fit.Target) string {
	switch t.Kind {
	case fit.TargetCPU:
		return "CPU"
	case fit.TargetGPU:
		return fmt.Sprintf("GPU %d", t.GPU)
	}
	return "GPU"
}
