package ui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/fit"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const gib = int64(1) << 30

func fitModel(t *testing.T, env map[string]string, client *dmr.Client) *Model {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{
		Config:   cfg,
		Client:   client,
		Renderer: lipgloss.NewRenderer(io.Discard),
		User:     "user",
		Version:  "1.1.0",
		Session:  context.Background(),
		Budget:   8192,
		Learned:  fit.LoadLearned(""),
	})
	m.width, m.height = 120, 40
	m.layout()
	return m
}

// denseModel is a 32-layer model with 8 KV heads of 128 (128 KiB of KV per
// token) and 4.6 GiB of weights, trained for 32k.
func denseModel() fit.ModelInfo {
	m := fit.ModelInfo{
		Arch: "llama", Layers: 32, KeyLength: 128, ValueLength: 128,
		Embedding: 4096, Vocab: 128256, WeightBytes: 46 * gib / 10, ContextLength: 32768,
		HeadCountKV: make([]int, 32),
	}
	for i := range m.HeadCountKV {
		m.HeadCountKV[i] = 8
	}
	return m
}

func probe(m *Model, info fit.ModelInfo, infoErr error, freeGiB ...int64) {
	msg := probeMsg{gen: m.gen, model: "docker.io/ai/example:8b", info: info, infoErr: infoErr}
	msg.snap.OK = len(freeGiB) > 0
	for i, f := range freeGiB {
		msg.snap.GPUs = append(msg.snap.GPUs, gpuGPU(i, f))
	}
	msg.ram.TotalBytes = 64 * gib
	msg.ram.AvailableBytes = 60 * gib
	msg.ram.CPUs = 16
	m.fit = &fitState{model: msg.model}
	m.Update(msg)
}

func TestContextChoicesAreCappedByModelMax(t *testing.T) {
	m := fitModel(t, nil, nil)
	probe(m, denseModel(), nil, 16)
	if m.screen != screenContext {
		t.Fatalf("screen %v", m.screen)
	}
	if got := m.fit.choices; len(got) != 3 || got[2] != 32768 {
		t.Fatalf("choices %v", got)
	}
	view := m.View()
	for _, want := range []string{"8k", "16k", "32k", "fits"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "64k") {
		t.Fatalf("64k offered above the model max:\n%s", view)
	}
	// DEFAULT_CONTEXT=auto preselects the largest that fits.
	if m.fit.ctx() != 32768 {
		t.Fatalf("preselected %d", m.fit.ctx())
	}
}

func TestNoFitBlocksLoadAndSuggestsFixes(t *testing.T) {
	m := fitModel(t, nil, nil)
	// 8 GiB free: 4.6 GiB weights + 4 GiB KV at 32k does not fit.
	probe(m, denseModel(), nil, 8)
	m.fit.ctxIdx = 2
	m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // context -> target (Auto, CPU)
	if m.screen != screenTarget {
		t.Fatalf("screen %v", m.screen)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenFit || m.fit.result.Verdict != fit.NoFit {
		t.Fatalf("screen %v verdict %v", m.screen, m.fit.result.Verdict)
	}
	if ok, _ := m.canLoad(); ok {
		t.Fatal("load allowed for a model that does not fit")
	}
	view := m.View()
	for _, want := range []string{"Not enough memory", "will not be loaded", "use ctx 8k", "run on CPU", "smaller model", "docker model configure --context-size 32768 ai/example:8b"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.screen != screenFit {
		t.Fatal("enter must not start loading")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m.fit.ctx() != 8192 || m.fit.result.Verdict != fit.Fits {
		t.Fatalf("suggestion: ctx %d verdict %v", m.fit.ctx(), m.fit.result.Verdict)
	}
	m.fit.ctxIdx = 2
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if m.fit.target().Kind != fit.TargetCPU || m.fit.result.Verdict != fit.Fits {
		t.Fatalf("cpu: %v %v", m.fit.target(), m.fit.result.Verdict)
	}
}

func TestUnknownNeedAsksFirstAndStrictRefuses(t *testing.T) {
	m := fitModel(t, map[string]string{"CONTEXT_SELECT": "off", "TARGET_SELECT": "off"}, nil)
	probe(m, fit.ModelInfo{}, fit.ErrNoStore, 16)
	if m.screen != screenFit || m.fit.result.Verdict != fit.Unknown {
		t.Fatalf("screen %v verdict %v", m.screen, m.fit.result.Verdict)
	}
	if ok, _ := m.canLoad(); ok {
		t.Fatal("unknown need loaded without confirmation")
	}
	if v := m.View(); !strings.Contains(v, "Press y to load anyway") || !strings.Contains(v, "not mounted") {
		t.Fatalf("view:\n%s", v)
	}

	s := fitModel(t, map[string]string{"FIT_CHECK": "strict", "CONTEXT_SELECT": "off", "TARGET_SELECT": "off"}, nil)
	probe(s, fit.ModelInfo{}, fit.ErrNoStore, 16)
	s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if s.screen != screenFit {
		t.Fatal("strict mode loaded an unknown model")
	}
}

func TestLearnedUsageReplacesUnknownHeader(t *testing.T) {
	m := fitModel(t, map[string]string{"CONTEXT_SELECT": "off", "TARGET_SELECT": "off", "DEFAULT_CONTEXT": "8k"}, nil)
	if _, err := m.learned.Record("docker.io/ai/example:8b", 8192, fit.Target{Kind: fit.TargetAuto}, map[int]int64{0: 6 * gib}); err != nil {
		t.Fatal(err)
	}
	probe(m, fit.ModelInfo{}, fit.ErrNoStore, 16)
	if m.fit.result.Verdict != fit.Fits || !m.fit.result.Estimate.Measured {
		t.Fatalf("verdict %v measured %v", m.fit.result.Verdict, m.fit.result.Estimate.Measured)
	}
	if !strings.Contains(m.View(), "measured") {
		t.Fatal("view should say the figure was measured")
	}
}

type fakeDMR struct {
	mu        sync.Mutex
	configure []string
	warms     int
	status    int
}

func (f *fakeDMR) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/engines/_configure":
			b, _ := io.ReadAll(r.Body)
			f.configure = append(f.configure, string(b))
			if f.status != 0 {
				http.Error(w, "runner already active", f.status)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "/engines/v1/chat/completions":
			f.warms++
			_, _ = io.WriteString(w, "{}")
		default:
			http.NotFound(w, r)
		}
	})
}

func dmrClient(t *testing.T, url string) *dmr.Client {
	c, err := dmr.New(dmr.Options{BaseURL: url, RequestTimeout: 2 * time.Second, WarmTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func run(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(tea.BatchMsg); ok || msg == nil {
			return
		}
		_, cmd = m.Update(msg)
	}
}

func TestLoadConfiguresThenWarms(t *testing.T) {
	fake := &fakeDMR{}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	m := fitModel(t, nil, dmrClient(t, srv.URL))
	probe(m, denseModel(), nil, 16, 16)
	m.fit.ctxIdx = 1 // 16k
	m.fit.tgtIdx = 2 // Auto, GPU 0, GPU 1, CPU
	_, cmd := m.evaluate()
	if cmd != nil || m.fit.result.Verdict != fit.Fits {
		t.Fatalf("verdict %v", m.fit.result.Verdict)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if m.screen != screenChat {
		t.Fatalf("screen %v err %q", m.screen, m.errText)
	}
	if len(fake.configure) != 1 || fake.configure[0] != `{"model":"docker.io/ai/example:8b","context-size":16384,"runtime-flags":["--device","CUDA1"]}` {
		t.Fatalf("configure %v", fake.configure)
	}
	if fake.warms != 1 {
		t.Fatalf("warms %d", fake.warms)
	}
	if m.budget != 12288 || m.ctxSize != 16384 {
		t.Fatalf("budget %d ctx %d", m.budget, m.ctxSize)
	}
	if !strings.Contains(m.View(), "ctx 16k · GPU 1") {
		t.Fatalf("chat header:\n%s", m.View())
	}
}

func TestBusyRunnerShowsClearMessage(t *testing.T) {
	fake := &fakeDMR{status: http.StatusConflict}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	m := fitModel(t, map[string]string{"CONTEXT_SELECT": "off", "TARGET_SELECT": "off"}, dmrClient(t, srv.URL))
	probe(m, denseModel(), nil, 16)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if m.screen != screenFit {
		t.Fatalf("screen %v", m.screen)
	}
	if fake.warms != 0 {
		t.Fatal("warmed after a refused configure")
	}
	if !strings.Contains(m.fit.errText, "Another session is using this model") {
		t.Fatalf("err %q", m.fit.errText)
	}
}

func TestNoConfigureWithoutPassingCheck(t *testing.T) {
	fake := &fakeDMR{}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	m := fitModel(t, map[string]string{"CONTEXT_SELECT": "off", "TARGET_SELECT": "off", "DEFAULT_CONTEXT": "32k", "ALLOW_CPU": "off"}, dmrClient(t, srv.URL))
	probe(m, denseModel(), nil, 6)
	for _, k := range []string{"enter", "y", "s", "p"} {
		var msg tea.KeyMsg
		if k == "enter" {
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd := m.Update(msg)
		if k == "s" {
			// The suggestion only re-evaluates; it never loads.
			if cmd != nil {
				t.Fatal("suggestion started a load")
			}
			m.fit.ctxIdx = len(m.fit.choices) - 1
			m.evaluate()
			continue
		}
		run(m, cmd)
	}
	if len(fake.configure) != 0 || fake.warms != 0 {
		t.Fatalf("DMR touched: configure %v warms %d", fake.configure, fake.warms)
	}
}

func TestFitOffKeepsPlainWarm(t *testing.T) {
	m := fitModel(t, map[string]string{"FIT_CHECK": "off", "CONTEXT_SELECT": "off", "TARGET_SELECT": "off"}, nil)
	if m.cfg.ConfiguresDMR() {
		t.Fatal("all fit features off should keep the old load path")
	}
	if budgetFor(0, 8192) != 8192 || budgetFor(32768, 8192) != 24576 || budgetFor(1024, 8192) != 1024 {
		t.Fatal("budgetFor")
	}
}

func gpuGPU(i int, freeGiB int64) gpuinfoGPU {
	return gpuinfoGPU{Index: i, Name: "Test GPU", MemTotalMiB: int(freeGiB*1024) + 400, MemUsedMiB: 400, MemFreeMiB: int(freeGiB * 1024)}
}

type gpuinfoGPU = gpuinfo.GPU
