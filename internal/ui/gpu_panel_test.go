package ui

import (
	"io"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/charmbracelet/lipgloss"
)

func TestHeatBandBoundaries(t *testing.T) {
	temp := []struct {
		v    float64
		want heat
	}{
		{0, heatCool},
		{49.9, heatCool},
		{50, heatWarn},
		{59.9, heatWarn},
		{60, heatHot},
		{69.9, heatHot},
		{70, heatCrit},
		{95, heatCrit},
	}
	for _, c := range temp {
		if got := heatBand(c.v, 50, 60, 70); got != c.want {
			t.Fatalf("temp %v -> %v, want %v", c.v, got, c.want)
		}
	}
	power := []struct {
		v    float64
		want heat
	}{
		{49.9, heatCool},
		{50, heatWarn},
		{74.9, heatWarn},
		{75, heatHot},
		{149.9, heatHot},
		{150, heatCrit},
		{220, heatCrit},
	}
	for _, c := range power {
		if got := heatBand(c.v, 50, 75, 150); got != c.want {
			t.Fatalf("power %v -> %v, want %v", c.v, got, c.want)
		}
	}
	mem := []struct {
		v    float64
		want heat
	}{
		{0, heatCool},
		{24.9, heatCool},
		{25, heatWarn},
		{49.9, heatWarn},
		{50, heatHot},
		{74.9, heatHot},
		{75, heatCrit},
		{91, heatCrit},
	}
	for _, c := range mem {
		if got := heatBand(c.v, 25, 50, 75); got != c.want {
			t.Fatalf("mem %v -> %v, want %v", c.v, got, c.want)
		}
	}
	if heatBand(60.06, 50, 60, 70) != heatHot {
		t.Fatal("60.06°C should be orange, not the integer-zero green")
	}
	if heatColor(heatHot).Dark != "208" {
		t.Fatalf("orange dark colour %q", heatColor(heatHot).Dark)
	}
	if heatColor(heatCool).Dark == "" || heatColor(heatWarn).Dark == "" || heatColor(heatCrit).Dark == "" {
		t.Fatal("missing heat colours")
	}
	if gpuBlue.Dark == "" {
		t.Fatal("missing name colour")
	}
}

func TestGPUPanelColumnsWrapAndCompact(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	th := config.GPU{TempWarn: 50, TempHot: 60, TempCrit: 70, PowerWarn: 50, PowerHot: 75, PowerCrit: 150}
	one := []gpuinfo.GPU{fakeGPU(0, "Ada", 40, 40, false)}
	two := []gpuinfo.GPU{fakeGPU(0, "Ada", 40, 40, false), fakeGPU(1, "Ada", 55, 80, true)}
	four := []gpuinfo.GPU{
		fakeGPU(0, "Ada", 40, 20, false),
		fakeGPU(1, "Ada", 55, 60, false),
		fakeGPU(2, "Ada", 65, 100, false),
		fakeGPU(3, "Ada", 80, 160, true),
	}

	panel := renderGPUPanel(r, one, 100, th, false)
	if lipgloss.Height(panel) != 4 || lipgloss.Width(panel) > 100 {
		t.Fatalf("1 gpu height %d width %d\n%s", lipgloss.Height(panel), lipgloss.Width(panel), panel)
	}
	if !strings.Contains(panel, "GPU0") || !strings.Contains(panel, "°C") {
		t.Fatalf("single column missing rows:\n%s", panel)
	}

	panel = renderGPUPanel(r, two, 100, th, false)
	first := strings.Split(panel, "\n")[0]
	if lipgloss.Height(panel) != 4 {
		t.Fatalf("2 gpus should share one row, height %d\n%s", lipgloss.Height(panel), panel)
	}
	if !strings.Contains(first, "GPU1") || !strings.Contains(first, "GPU0") {
		t.Fatalf("loaded GPU should sit beside the other on the first row:\n%s", first)
	}
	if !strings.Contains(first, "GPU1") || !strings.Contains(panel, "*") {
		t.Fatalf("loaded GPU should be marked:\n%s", panel)
	}
	// Loaded GPU is ordered first.
	if strings.Index(first, "GPU1") > strings.Index(first, "GPU0") {
		t.Fatalf("loaded GPU should be the left column:\n%s", first)
	}

	wide := renderGPUPanel(r, four, 120, th, false)
	top := strings.Split(wide, "\n")[0]
	if lipgloss.Height(wide) != 4 || lipgloss.Width(wide) > 120 {
		t.Fatalf("4 gpus wide height %d width %d\n%s", lipgloss.Height(wide), lipgloss.Width(wide), wide)
	}
	for _, name := range []string{"GPU0", "GPU1", "GPU2", "GPU3"} {
		if !strings.Contains(top, name) {
			t.Fatalf("wide row missing %s:\n%s", name, top)
		}
	}

	wrapped := renderGPUPanel(r, four, 50, th, false)
	if lipgloss.Height(wrapped) != 8 || lipgloss.Width(wrapped) > 50 {
		t.Fatalf("4 gpus wrapped height %d width %d\n%s", lipgloss.Height(wrapped), lipgloss.Width(wrapped), wrapped)
	}
	wlines := strings.Split(wrapped, "\n")
	if strings.Contains(wlines[0], "GPU0") && strings.Contains(wlines[0], "GPU1") && strings.Contains(wlines[0], "GPU2") {
		t.Fatalf("medium width should wrap, first line:\n%s", wlines[0])
	}

	narrow := renderGPUPanel(r, four, 24, th, false)
	if lipgloss.Height(narrow) != 4 || lipgloss.Width(narrow) > 24 {
		t.Fatalf("compact height %d width %d\n%s", lipgloss.Height(narrow), lipgloss.Width(narrow), narrow)
	}
	nlines := strings.Split(narrow, "\n")
	seen := map[string]bool{}
	for _, ln := range nlines {
		if strings.Contains(ln, "\n") {
			t.Fatal("compact line wrapped")
		}
		for _, name := range []string{"GPU0", "GPU1", "GPU2", "GPU3"} {
			if strings.Contains(ln, name) {
				seen[name] = true
			}
		}
	}
	if len(seen) != 4 {
		t.Fatalf("compact lines %v\n%s", seen, narrow)
	}
}

func TestGPUPanelInViewKeepsHeight(t *testing.T) {
	m := testModel(t)
	p := gpuinfo.NewPoller("auto", "nvidia-smi", 0)
	p.SetSnapshot(gpuinfo.Snapshot{OK: true, GPUs: []gpuinfo.GPU{
		fakeGPU(0, "Ada", 42, 30, false),
		fakeGPU(1, "Ada", 71, 160, true),
	}})
	m.gpu = p
	m.screen = screenChat
	for _, width := range []int{100, 40, 28} {
		m.width = width
		m.height = 24
		view := m.View()
		if lipgloss.Height(view) != m.height {
			t.Fatalf("width %d height %d\n%s", width, lipgloss.Height(view), view)
		}
		if lipgloss.Width(view) > width {
			t.Fatalf("width %d rendered %d", width, lipgloss.Width(view))
		}
	}
}

func fakeGPU(index int, name string, temp int, watts float64, loaded bool) gpuinfo.GPU {
	g := gpuinfo.GPU{
		Index:       index,
		Name:        "NVIDIA RTX " + name,
		MemUsedMiB:  4096,
		MemTotalMiB: 20480,
		TempC:       float64(temp),
		TempKnown:   true,
		PowerW:      watts,
		PowerKnown:  true,
	}
	if loaded {
		g.AppsMiB = 14000
	}
	return g
}
