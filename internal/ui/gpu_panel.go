package ui

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Heat bands. A value v is
//
//	green  when v < warn
//	yellow when warn ≤ v < hot
//	orange when hot ≤ v < crit
//	red    when v ≥ crit
type heat int

const (
	heatCool heat = iota
	heatWarn
	heatHot
	heatCrit
)

var (
	gpuBlue    = lipgloss.AdaptiveColor{Light: "19", Dark: "33"}
	gpuGreen   = lipgloss.AdaptiveColor{Light: "22", Dark: "40"}
	gpuYellow  = lipgloss.AdaptiveColor{Light: "136", Dark: "220"}
	gpuOrange  = lipgloss.AdaptiveColor{Light: "166", Dark: "208"}
	gpuRed     = lipgloss.AdaptiveColor{Light: "124", Dark: "196"}
	gpuNeutral = lipgloss.AdaptiveColor{Light: "240", Dark: "252"}
)

func heatBand(value, warn, hot, crit float64) heat {
	if value < warn {
		return heatCool
	}
	if value < hot {
		return heatWarn
	}
	if value < crit {
		return heatHot
	}
	return heatCrit
}

func heatColor(h heat) lipgloss.AdaptiveColor {
	switch h {
	case heatCool:
		return gpuGreen
	case heatWarn:
		return gpuYellow
	case heatHot:
		return gpuOrange
	default:
		return gpuRed
	}
}

func (m *Model) gpuThresholds() config.GPU {
	g := m.cfg.GPU
	if g.TempWarn <= 0 {
		g.TempWarn, g.TempHot, g.TempCrit = 50, 60, 70
	}
	if g.PowerWarn <= 0 {
		g.PowerWarn, g.PowerHot, g.PowerCrit = 50, 75, 150
	}
	if g.MemWarn <= 0 {
		g.MemWarn, g.MemHot, g.MemCrit = 25, 50, 75
	}
	return g
}

func memPercent(used, total int) float64 {
	if total <= 0 {
		return 0
	}
	return 100 * float64(used) / float64(total)
}

func formatVRAM(used, total int) string {
	pct := int(math.Round(memPercent(used, total)))
	return fmt.Sprintf("%d/%d MiB %d%%", used, total, pct)
}

func memHeat(used, total int, th config.GPU) heat {
	return heatBand(memPercent(used, total), th.MemWarn, th.MemHot, th.MemCrit)
}

// renderGPUPanel lays GPUs out as columns. Each column is four rows: name,
// VRAM, temperature, power. Columns sit side by side. When they do not fit
// on one row they wrap, up to two block-rows. Narrower terminals get one
// coloured line per GPU. forceCompact skips the columns.
func renderGPUPanel(r *lipgloss.Renderer, gpus []gpuinfo.GPU, width int, th config.GPU, forceCompact bool) string {
	if r == nil {
		r = lipgloss.NewRenderer(io.Discard)
	}
	if width < 1 {
		width = 1
	}
	ordered := orderGPUs(gpus)
	if len(ordered) == 0 {
		return ""
	}
	if !forceCompact {
		if panel, ok := joinGPUColumns(r, ordered, width, th); ok {
			return panel
		}
	}
	lines := make([]string, len(ordered))
	for i, g := range ordered {
		lines[i] = clipPlainWidth(compactGPULine(r, g, th), width)
	}
	return strings.Join(lines, "\n")
}

func orderGPUs(gpus []gpuinfo.GPU) []gpuinfo.GPU {
	out := append([]gpuinfo.GPU(nil), gpus...)
	sort.SliceStable(out, func(i, j int) bool {
		li := out[i].AppsMiB > 0
		lj := out[j].AppsMiB > 0
		if li != lj {
			return li
		}
		return out[i].Index < out[j].Index
	})
	return out
}

func joinGPUColumns(r *lipgloss.Renderer, gpus []gpuinfo.GPU, width int, th config.GPU) (string, bool) {
	cols := make([]string, len(gpus))
	widths := make([]int, len(gpus))
	for i, g := range gpus {
		cols[i] = gpuColumn(r, g, th)
		widths[i] = lipgloss.Width(cols[i])
		if widths[i] > width {
			return "", false
		}
	}
	gap := columnGap(4)
	gapW := lipgloss.Width(strings.Split(gap, "\n")[0])
	var rows []string
	for i := 0; i < len(cols); {
		used := 0
		var batch []string
		for i < len(cols) {
			add := widths[i]
			if len(batch) > 0 {
				add += gapW
			}
			if len(batch) > 0 && used+add > width {
				break
			}
			batch = append(batch, cols[i])
			used += add
			i++
		}
		parts := make([]string, 0, len(batch)*2)
		for j, col := range batch {
			if j > 0 {
				parts = append(parts, gap)
			}
			parts = append(parts, col)
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
	}
	// One extra block-row is the wrap. More than that is the compact fallback.
	if len(rows) > 2 {
		return "", false
	}
	return strings.Join(rows, "\n"), true
}

func columnGap(height int) string {
	if height < 1 {
		height = 1
	}
	lines := make([]string, height)
	for i := range lines {
		lines[i] = " │ "
	}
	return strings.Join(lines, "\n")
}

func gpuColumn(r *lipgloss.Renderer, g gpuinfo.GPU, th config.GPU) string {
	name := gpuNameLine(g)
	vram := formatVRAM(g.MemUsedMiB, g.MemTotalMiB)
	temp := "n/a"
	tempColour := gpuNeutral
	if g.TempKnown {
		temp = fmt.Sprintf("%d°C", int(math.Round(g.TempC)))
		tempColour = heatColor(heatBand(g.TempC, th.TempWarn, th.TempHot, th.TempCrit))
	}
	power := "n/a"
	powerColour := gpuNeutral
	if g.PowerKnown {
		power = fmt.Sprintf("%.0fW", g.PowerW)
		powerColour = heatColor(heatBand(g.PowerW, th.PowerWarn, th.PowerHot, th.PowerCrit))
	}
	nameStyle := r.NewStyle().Foreground(gpuBlue)
	if g.AppsMiB > 0 {
		nameStyle = nameStyle.Bold(true)
	}
	lines := []string{
		nameStyle.Render(name),
		r.NewStyle().Foreground(heatColor(memHeat(g.MemUsedMiB, g.MemTotalMiB, th))).Render(vram),
		r.NewStyle().Foreground(tempColour).Render(temp),
		r.NewStyle().Foreground(powerColour).Render(power),
	}
	w := 0
	for _, ln := range lines {
		if n := lipgloss.Width(ln); n > w {
			w = n
		}
	}
	for i, ln := range lines {
		if pad := w - lipgloss.Width(ln); pad > 0 {
			lines[i] = ln + strings.Repeat(" ", pad)
		}
	}
	return strings.Join(lines, "\n")
}

func gpuNameLine(g gpuinfo.GPU) string {
	name := strings.TrimPrefix(strings.TrimSpace(g.Name), "NVIDIA ")
	if name == "" {
		name = "GPU"
	}
	label := fmt.Sprintf("GPU%d %s", g.Index, name)
	if g.AppsMiB > 0 {
		label += "*"
	}
	return clipRunes(label, 18)
}

func compactGPULine(r *lipgloss.Renderer, g gpuinfo.GPU, th config.GPU) string {
	name := r.NewStyle().Foreground(gpuBlue)
	if g.AppsMiB > 0 {
		name = name.Bold(true)
	}
	tempText := "n/a"
	tempColour := gpuNeutral
	if g.TempKnown {
		tempText = fmt.Sprintf("%d°C", int(math.Round(g.TempC)))
		tempColour = heatColor(heatBand(g.TempC, th.TempWarn, th.TempHot, th.TempCrit))
	}
	vram := r.NewStyle().Foreground(heatColor(memHeat(g.MemUsedMiB, g.MemTotalMiB, th))).Render(formatVRAM(g.MemUsedMiB, g.MemTotalMiB))
	temp := r.NewStyle().Foreground(tempColour).Render(tempText)
	power := r.NewStyle().Foreground(gpuNeutral).Render("n/a")
	if g.PowerKnown {
		power = r.NewStyle().Foreground(heatColor(heatBand(g.PowerW, th.PowerWarn, th.PowerHot, th.PowerCrit))).Render(fmt.Sprintf("%.0fW", g.PowerW))
	}
	return name.Render(gpuNameLine(g)) + " " + vram + " " + temp + " " + power
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

func clipPlainWidth(s string, width int) string {
	if width < 1 || lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}
