package ui

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/edits"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/UnixEU/ssh-llm-tui/internal/sysstat"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
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

func TestStatusRowsFormat(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	th := config.GPU{TempWarn: 50, TempHot: 60, TempCrit: 70, PowerWarn: 50, PowerHot: 75, PowerCrit: 150, MemWarn: 40, MemHot: 60, MemCrit: 90}
	sys := &sysstat.Snapshot{CPUKnown: true, CPUPercent: 47.6, MemKnown: true, MemUsed: 32 << 30, MemTotal: 128 << 30}
	g := fakeGPU(0, "Ada", 40, 40, false)
	g.UtilPct, g.UtilKnown = 37.4, true
	lines := renderStatus(r, statusRows([]gpuinfo.GPU{g}, sys, "", th), 200, 4)
	if len(lines) != 2 {
		t.Fatalf("rows %q", lines)
	}
	if !strings.HasPrefix(lines[0], "Host") || !strings.Contains(lines[0], "RAM 32/128 GiB") || !strings.Contains(lines[0], "CPU 48%") {
		t.Fatalf("host row %q", lines[0])
	}
	for _, want := range []string{"GPU0", "RTX Ada", "4.0/20.0 GiB", "37%", "40°C", "40W"} {
		if !strings.Contains(lines[1], want) {
			t.Fatalf("gpu row %q lacks %q", lines[1], want)
		}
	}
	if formatVRAM(1024, 24576) != "1.0/24.0 GiB" || formatVRAM(4096, 8192) != "4096/8192 MiB" {
		t.Fatal(formatVRAM(1024, 24576), formatVRAM(4096, 8192))
	}
	small := sysstat.Snapshot{MemKnown: true, MemUsed: 3 << 29, MemTotal: 8 << 30}
	if cpuText(small) != "CPU …" || ramText(small) != "RAM 1.5/8.0 GiB" {
		t.Fatalf("%q %q", cpuText(small), ramText(small))
	}
	big := sysstat.Snapshot{MemKnown: true, MemUsed: 11 << 30, MemTotal: 720 << 30}
	if ramText(big) != "RAM 11/720 GiB" {
		t.Fatal(ramText(big))
	}
	if renderStatus(r, statusRows(nil, &sysstat.Snapshot{}, "", th), 100, 4) != nil {
		t.Fatal("empty snapshot should render nothing")
	}
	// Busy GPUs come first and are marked.
	busy := renderStatus(r, statusRows([]gpuinfo.GPU{fakeGPU(0, "Ada", 40, 40, false), fakeGPU(1, "Ada", 40, 40, true)}, nil, "", th), 200, 4)
	if !strings.HasPrefix(busy[0], "GPU1*") || !strings.HasPrefix(busy[1], "GPU0 ") {
		t.Fatalf("order %q", busy)
	}
	// A failed GPU poll is a row of its own.
	na := renderStatus(r, statusRows(nil, sys, "n/a", th), 200, 4)
	if len(na) != 2 || !strings.HasPrefix(na[1], "GPU") || !strings.Contains(na[1], "n/a") {
		t.Fatalf("note %q", na)
	}
}

// TestStatusBlockMatrix renders the header with 1, 2, 4 and 8 GPUs at
// several widths: no line may be wider than the terminal, the block has a
// fixed height, its rows share one left edge and its columns line up.
func TestStatusBlockMatrix(t *testing.T) {
	m := testModel(t)
	m.sys = sysstat.NewPoller(0)
	m.sys.SetSnapshot(sysstat.Snapshot{CPUKnown: true, CPUPercent: 1, MemKnown: true, MemUsed: 11 << 30, MemTotal: 720 << 30})
	title := "Docker Hub · ai/"
	right := "model disk: 2.8 TiB free"
	for _, n := range []int{1, 2, 4, 8} {
		var gpus []gpuinfo.GPU
		for i := 0; i < n; i++ {
			g := fakeGPU(i, "Ada", 25+i*9, float64(24+i*40), i == 1)
			g.Name = []string{"NVIDIA GeForce RTX 4090", "NVIDIA RTX 6000 Ada Generation", "Tesla T4", "NVIDIA A100-SXM4-80GB"}[i%4]
			g.MemTotalMiB = []int{24564, 49140, 15360, 81920}[i%4]
			g.MemUsedMiB = g.MemTotalMiB / (i + 2)
			g.UtilPct, g.UtilKnown = float64(i*13), true
			gpus = append(gpus, g)
		}
		p := gpuinfo.NewPoller("auto", "nvidia-smi", 0)
		p.SetSnapshot(gpuinfo.Snapshot{OK: true, GPUs: gpus})
		m.gpu = p
		wantH := min(1+n, m.statusMaxRows())
		for _, width := range []int{80, 100, 140, 145, 200} {
			m.width = width
			for _, block := range []string{m.headerBlock(title, right), m.fitHeader(title, right, 10)} {
				lines := strings.Split(block, "\n")
				if len(lines) != wantH {
					t.Fatalf("%d gpus width %d: %d lines, want %d\n%s", n, width, len(lines), wantH, block)
				}
				for _, ln := range lines {
					if w := lipgloss.Width(ln); w > width {
						t.Fatalf("%d gpus width %d: line %d wide\n%s", n, width, w, block)
					}
				}
				if !strings.HasPrefix(lines[0], "Docker Hub") {
					t.Fatalf("%d gpus width %d: title lost\n%s", n, width, block)
				}
				checkStatusColumns(t, lines, n, width)
			}
			if n > 3 {
				last := lines(m.headerBlock(title, right))[wantH-1]
				if want := fmt.Sprintf("+%d more GPUs", n-(wantH-2)); !strings.Contains(last, want) {
					t.Fatalf("%d gpus width %d: last row %q, want %q", n, width, last, want)
				}
			}
		}
	}
}

func lines(s string) []string { return strings.Split(s, "\n") }

// checkStatusColumns finds the status block (it starts at "Host") and
// checks that every row starts at the same column, the memory cells end at
// the same column and, on GPU rows, temperature and power line up.
func checkStatusColumns(t *testing.T, rows []string, n, width int) {
	t.Helper()
	host := []rune(rows[0])
	start := strings.Index(string(host), "Host")
	if start < 0 {
		t.Fatalf("%d gpus width %d: no host row %q", n, width, rows[0])
	}
	start = len([]rune(rows[0][:start]))
	memEnd, tempEnd := -1, -1
	for i, row := range rows {
		rr := []rune(row)
		if strings.Contains(row, "more GPU") {
			continue
		}
		label := strings.TrimSpace(string(rr[start:min(start+5, len(rr))]))
		if i > 0 && !strings.HasPrefix(label, "GPU") {
			t.Fatalf("%d gpus width %d: row %d starts with %q, want a GPU label at col %d\n%s", n, width, i, label, start, strings.Join(rows, "\n"))
		}
		if i > 0 && start > 0 && rr[start-1] != ' ' {
			t.Fatalf("row %d does not start at the block edge: %q", i, row)
		}
		if e := runeEnd(row, "GiB"); e >= 0 {
			if memEnd >= 0 && e != memEnd {
				t.Fatalf("%d gpus width %d: memory column ends at %d and %d\n%s", n, width, memEnd, e, strings.Join(rows, "\n"))
			}
			memEnd = e
		}
		if e := runeEnd(row, "°C"); e >= 0 {
			if tempEnd >= 0 && e != tempEnd {
				t.Fatalf("%d gpus width %d: temperature column ends at %d and %d\n%s", n, width, tempEnd, e, strings.Join(rows, "\n"))
			}
			tempEnd = e
		}
	}
	if memEnd < 0 {
		t.Fatalf("%d gpus width %d: no memory column\n%s", n, width, strings.Join(rows, "\n"))
	}
	// Equal-width rows: every row's block ends at the same column.
	end := lipgloss.Width(rows[len(rows)-1])
	for _, row := range rows {
		if lipgloss.Width(row) != end {
			t.Fatalf("%d gpus width %d: ragged rows\n%s", n, width, strings.Join(rows, "\n"))
		}
	}
}

func runeEnd(s, sub string) int {
	i := strings.Index(s, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(s[:i])) + len([]rune(sub))
}

func TestStatusBlockNarrowAndShort(t *testing.T) {
	m := testModel(t)
	var gpus []gpuinfo.GPU
	for i := 0; i < 8; i++ {
		gpus = append(gpus, fakeGPU(i, "Ada", 40, 40, false))
	}
	p := gpuinfo.NewPoller("auto", "nvidia-smi", 0)
	p.SetSnapshot(gpuinfo.Snapshot{OK: true, GPUs: gpus})
	m.gpu = p
	for _, width := range []int{20, 30, 40, 60} {
		m.width = width
		block := m.fitHeader("A rather long screen title that will not fit", "with extra", 2)
		ls := lines(block)
		if len(ls) > 2 {
			t.Fatalf("width %d: %d lines\n%s", width, len(ls), block)
		}
		for _, ln := range ls {
			if lipgloss.Width(ln) > width {
				t.Fatalf("width %d: %q is %d wide", width, ln, lipgloss.Width(ln))
			}
		}
	}
	m.width = 60
	ls := lines(m.fitHeader("Title", "", 2))
	if len(ls) != 2 || !strings.Contains(ls[1], "+7 more GPUs") {
		t.Fatalf("2-row cap %q", ls)
	}
}

// TestStatusBlockSameOnEveryScreen checks that each screen draws the same
// status rows at the top.
func TestStatusBlockSameOnEveryScreen(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	m.files = uploads.New(t.TempDir(), 1<<20, 100)
	m.edits = edits.NewManager()
	var gpus []gpuinfo.GPU
	for i := 0; i < 8; i++ {
		gpus = append(gpus, fakeGPU(i, "Ada", 40+i, 40, i == 2))
	}
	p := gpuinfo.NewPoller("auto", "nvidia-smi", 0)
	p.SetSnapshot(gpuinfo.Snapshot{OK: true, GPUs: gpus})
	m.gpu = p
	m.width, m.height = 140, 30
	want := ""
	for _, sc := range []screen{screenWelcome, screenModels, screenChat, screenFiles, screenChanges, screenCtxInfo, screenHub} {
		if sc == screenHub {
			m.screen = screenModels
			f.open(t)
		}
		m.screen = sc
		view := m.View()
		if lipgloss.Height(view) != m.height {
			t.Fatalf("screen %v height %d", sc, lipgloss.Height(view))
		}
		ls := lines(view)
		// Rows 2..4 are the status block alone.
		got := strings.Join(ls[1:4], "\n")
		if !strings.Contains(ls[3], "+5 more GPUs") {
			t.Fatalf("screen %v status rows:\n%s", sc, strings.Join(ls[:5], "\n"))
		}
		if want == "" {
			want = got
		} else if got != want {
			t.Fatalf("screen %v status differs:\n%s\nwant\n%s", sc, got, want)
		}
	}
}
