package ui

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/UnixEU/ssh-llm-tui/internal/sysstat"
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
		g.TempWarn, g.TempHot, g.TempCrit = 40, 60, 70
	}
	if g.PowerWarn <= 0 {
		g.PowerWarn, g.PowerHot, g.PowerCrit = 25, 100, 200
	}
	if g.MemWarn <= 0 {
		g.MemWarn, g.MemHot, g.MemCrit = 40, 60, 90
	}
	return g
}

func memPercent(used, total int) float64 {
	if total <= 0 {
		return 0
	}
	return 100 * float64(used) / float64(total)
}

func memHeat(used, total int, th config.GPU) heat {
	return heatBand(memPercent(used, total), th.MemWarn, th.MemHot, th.MemCrit)
}

// formatVRAM is "1.0/24.0 GiB" when the card has 10 GiB or more, else
// "4096/8192 MiB".
func formatVRAM(used, total int) string {
	if total >= 10*1024 {
		return fmt.Sprintf("%.1f/%.1f GiB", float64(used)/1024, float64(total)/1024)
	}
	return fmt.Sprintf("%d/%d MiB", used, total)
}

// The status block in the top-right corner is a small table: one row for
// the host (CPU and RAM) and one per GPU, with the columns
//
//	label  name  memory  load  temp  power
//
// Every row of a block has the same width and every column is aligned,
// so the block can be right-aligned as a whole. It never wraps: on a
// narrow terminal columns are dropped (power, then temperature, then the
// GPU name is shortened and dropped), and past the row limit the last row
// says how many GPUs are hidden.

const (
	colLabel = iota
	colName
	colMem
	colLoad
	colTemp
	colPower
	numCols
)

type statusCell struct {
	text  string
	color lipgloss.TerminalColor
	bold  bool
}

type statusRow struct {
	cells [numCols]statusCell
	gpu   bool
}

// statusLevel is one step of graceful degradation.
type statusLevel struct {
	nameMax int // 0 hides the name column
	hide    [numCols]bool
}

var statusLevels = []statusLevel{
	{nameMax: 22},
	{nameMax: 12},
	{nameMax: 0},
	{nameMax: 0, hide: [numCols]bool{colPower: true}},
	{nameMax: 0, hide: [numCols]bool{colPower: true, colTemp: true}},
	{nameMax: 0, hide: [numCols]bool{colPower: true, colTemp: true, colLoad: true}},
}

const statusGap = "  "

// statusRows builds the table rows: the host first, then GPUs with the
// busy ones first. note is shown as a GPU row when GPU stats failed.
func statusRows(gpus []gpuinfo.GPU, sys *sysstat.Snapshot, note string, th config.GPU) []statusRow {
	var rows []statusRow
	if sys != nil && (sys.CPUKnown || sys.MemKnown) {
		cpuC, ramC := sysHeats(*sys, th)
		var row statusRow
		row.cells[colLabel] = statusCell{text: "Host"}
		row.cells[colMem] = statusCell{text: ramText(*sys), color: ramC}
		row.cells[colLoad] = statusCell{text: cpuText(*sys), color: cpuC}
		rows = append(rows, row)
	}
	for _, g := range orderGPUs(gpus) {
		row := statusRow{gpu: true}
		label := fmt.Sprintf("GPU%d", g.Index)
		if g.AppsMiB > 0 {
			label += "*"
		}
		name := strings.TrimPrefix(strings.TrimSpace(g.Name), "NVIDIA ")
		row.cells[colLabel] = statusCell{text: label, bold: g.AppsMiB > 0}
		row.cells[colName] = statusCell{text: name, bold: g.AppsMiB > 0}
		row.cells[colMem] = statusCell{text: formatVRAM(g.MemUsedMiB, g.MemTotalMiB), color: heatColor(memHeat(g.MemUsedMiB, g.MemTotalMiB, th))}
		if g.UtilKnown {
			row.cells[colLoad] = statusCell{text: fmt.Sprintf("%d%%", int(math.Round(g.UtilPct))), color: heatColor(heatBand(g.UtilPct, th.MemWarn, th.MemHot, th.MemCrit))}
		}
		if g.TempKnown {
			row.cells[colTemp] = statusCell{text: fmt.Sprintf("%d°C", int(math.Round(g.TempC))), color: heatColor(heatBand(g.TempC, th.TempWarn, th.TempHot, th.TempCrit))}
		} else {
			row.cells[colTemp] = statusCell{text: "n/a", color: gpuNeutral}
		}
		if g.PowerKnown {
			row.cells[colPower] = statusCell{text: fmt.Sprintf("%.0fW", g.PowerW), color: heatColor(heatBand(g.PowerW, th.PowerWarn, th.PowerHot, th.PowerCrit))}
		} else {
			row.cells[colPower] = statusCell{text: "n/a", color: gpuNeutral}
		}
		rows = append(rows, row)
	}
	if note != "" {
		var row statusRow
		row.gpu = true
		row.cells[colLabel] = statusCell{text: "GPU"}
		row.cells[colMem] = statusCell{text: note, color: gpuNeutral}
		rows = append(rows, row)
	}
	return rows
}

// statusWidths is the width of each visible column at a level, and the
// total line width.
func statusWidths(rows []statusRow, lv statusLevel) ([numCols]int, int) {
	var w [numCols]int
	for _, row := range rows {
		for c := 0; c < numCols; c++ {
			if lv.hide[c] || (c == colName && lv.nameMax == 0) {
				continue
			}
			t := row.cells[c].text
			if c == colName {
				t = clipRunes(t, lv.nameMax)
			}
			if n := lipgloss.Width(t); n > w[c] {
				w[c] = n
			}
		}
	}
	total, cols := 0, 0
	for _, n := range w {
		if n > 0 {
			total += n
			cols++
		}
	}
	if cols > 1 {
		total += (cols - 1) * lipgloss.Width(statusGap)
	}
	return w, total
}

// renderStatus draws rows as at most maxRows lines of equal width, no
// wider than width. It returns nil when there is nothing to show.
func renderStatus(r *lipgloss.Renderer, rows []statusRow, width, maxRows int) []string {
	if len(rows) == 0 || width < 1 {
		return nil
	}
	if r == nil {
		r = lipgloss.NewRenderer(io.Discard)
	}
	if maxRows < 1 {
		maxRows = 1
	}
	hidden := 0
	if len(rows) > maxRows {
		keep := maxRows - 1
		if keep < 1 {
			keep = 1
		}
		for _, row := range rows[keep:] {
			if row.gpu {
				hidden++
			}
		}
		rows = rows[:keep]
		if maxRows == 1 {
			hidden = 0
		}
	}
	lv := statusLevels[len(statusLevels)-1]
	var w [numCols]int
	total := 0
	for _, cand := range statusLevels {
		cw, ct := statusWidths(rows, cand)
		lv, w, total = cand, cw, ct
		if ct <= width {
			break
		}
	}
	more := ""
	if hidden > 0 {
		more = fmt.Sprintf("+%d more %s", hidden, map[bool]string{true: "GPU", false: "GPUs"}[hidden == 1])
		if n := lipgloss.Width(more); n > total {
			total = n
		}
	}
	if total > width {
		total = width
	}
	out := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		var parts []string
		for c := 0; c < numCols; c++ {
			if w[c] == 0 {
				continue
			}
			cell := row.cells[c]
			t := cell.text
			if c == colName {
				t = clipRunes(t, lv.nameMax)
			}
			pad := strings.Repeat(" ", w[c]-lipgloss.Width(t))
			st := r.NewStyle().Bold(cell.bold)
			if cell.color != nil {
				st = st.Foreground(cell.color)
			}
			styled := t
			if t != "" {
				styled = st.Render(t)
			}
			if c == colLabel || c == colName {
				parts = append(parts, styled+pad)
			} else {
				parts = append(parts, pad+styled)
			}
		}
		out = append(out, fitCells(strings.Join(parts, statusGap), total))
	}
	if more != "" {
		line := r.NewStyle().Foreground(gpuNeutral).Render(more)
		out = append(out, fitCells(strings.Repeat(" ", max(0, total-lipgloss.Width(more)))+line, total))
	}
	return out
}

// fitCells pads or truncates s to exactly width cells.
func fitCells(s string, width int) string {
	if lipgloss.Width(s) > width {
		s = ansi.Truncate(s, width, "")
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// minStatusWidth is the narrowest the block can get for rows.
func minStatusWidth(rows []statusRow) int {
	_, n := statusWidths(rows, statusLevels[len(statusLevels)-1])
	return n
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

// cpuText is "CPU 48%", or "CPU …" before the second sample.
func cpuText(s sysstat.Snapshot) string {
	if !s.CPUKnown {
		return "CPU …"
	}
	return fmt.Sprintf("CPU %d%%", int(math.Round(s.CPUPercent)))
}

// ramText is "RAM 32/126 GiB", one decimal below 10 GiB.
func ramText(s sysstat.Snapshot) string {
	if !s.MemKnown {
		return "RAM n/a"
	}
	used := float64(s.MemUsed) / float64(1<<30)
	total := float64(s.MemTotal) / float64(1<<30)
	if total < 10 {
		return fmt.Sprintf("RAM %.1f/%.1f GiB", used, total)
	}
	return fmt.Sprintf("RAM %.0f/%.0f GiB", used, total)
}

func sysHeats(s sysstat.Snapshot, th config.GPU) (cpu, ram lipgloss.AdaptiveColor) {
	cpu, ram = gpuNeutral, gpuNeutral
	if s.CPUKnown {
		cpu = heatColor(heatBand(s.CPUPercent, th.MemWarn, th.MemHot, th.MemCrit))
	}
	if s.MemKnown && s.MemTotal > 0 {
		ram = heatColor(heatBand(float64(s.MemUsed)/float64(s.MemTotal)*100, th.MemWarn, th.MemHot, th.MemCrit))
	}
	return cpu, ram
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
