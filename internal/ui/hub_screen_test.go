package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/diskspace"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/hub"
	"github.com/UnixEU/ssh-llm-tui/internal/pull"
	"github.com/charmbracelet/lipgloss"
)

type hubFixture struct {
	m       *Model
	pulls   *pull.Manager
	mu      sync.Mutex
	pulled  []string
	release chan struct{}
}

func newHubFixture(t *testing.T, free int64) *hubFixture {
	t.Helper()
	f := &hubFixture{release: make(chan struct{})}
	t.Cleanup(func() { close(f.release) })
	hubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/repositories/ai/":
			fmt.Fprint(w, `{"results":[{"name":"gemma3","pull_count":1166136},{"name":"qwen3","pull_count":800000,"description":"Qwen3 language models"},{"name":"smollm2","pull_count":944356}]}`)
		case "/v2/repositories/ai/qwen3/tags":
			fmt.Fprint(w, `{"results":[{"name":"latest","full_size":5027784512},{"name":"8b-q8_0","full_size":8709519168},{"name":"8b-mlx-bf16","full_size":1}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hubSrv.Close)
	dmrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/engines/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"docker.io/ai/qwen3:latest"}]}`)
		case "/models/create":
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.pulled = append(f.pulled, string(b))
			f.mu.Unlock()
			fmt.Fprintln(w, `{"type":"progress","total":8709519168,"layer":{"id":"a","size":8709519168,"current":4354759584}}`)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-f.release:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(dmrSrv.Close)
	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	client, err := dmr.New(dmr.Options{BaseURL: dmrSrv.URL, RequestTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f.pulls = pull.New(ctx, client, 1)
	f.m = New(Options{
		Config: cfg, Client: client, Renderer: lipgloss.NewRenderer(io.Discard), User: "user", Session: context.Background(),
		Hub:   hub.New(hub.Options{BaseURL: hubSrv.URL}),
		Pulls: f.pulls,
		DiskFree: func() (diskspace.Info, error) {
			return diskspace.Info{TotalBytes: 100 << 30, FreeBytes: free}, nil
		},
	})
	f.m.width, f.m.height = 120, 32
	f.m.screen = screenModels
	f.m.models = []dmr.Model{{ID: "docker.io/ai/qwen3:latest"}}
	return f
}

// open goes to the hub screen, loads the repos and searches for qwen.
func (f *hubFixture) open(t *testing.T) {
	t.Helper()
	m := f.m
	key(m, "p")
	if m.screen != screenHub {
		t.Fatal("p should open the Docker Hub screen")
	}
	m.Update(m.fetchRepos()())
	if len(m.hub.shown) != 3 || m.hub.shown[0].Name != "gemma3" {
		t.Fatalf("repos = %+v", m.hub.shown)
	}
	for _, r := range "qwen" {
		key(m, string(r))
	}
	if len(m.hub.shown) != 1 || m.hub.shown[0].Name != "qwen3" {
		t.Fatalf("search = %+v", m.hub.shown)
	}
	key(m, "enter") // to the results
	if cmd := m.loadTags(); cmd != nil {
		t.Fatal("tags should already be loading")
	}
	m.Update(m.fetchTags("qwen3")())
	key(m, "enter") // to the tags
}

func TestHubSearchTagsAndPull(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	f.open(t)
	v := m.View()
	for _, want := range []string{"Docker Hub · ai/", "model disk: 50 GiB free", "ai/qwen3", "Qwen3 language models", "latest", "4.7 GiB", "✓ pulled", "8b-q8_0", "8.1 GiB"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "mlx") {
		t.Fatal("mlx tags should be hidden")
	}
	key(m, "j")
	key(m, "enter")
	v = m.View()
	for _, want := range []string{"Pull ai/qwen3:8b-q8_0", "Download size", "8.1 GiB", "Kept free", "2 GiB", "Needed", "10.1 GiB", "Free on model disk", "50 GiB", "Enough space."} {
		if !strings.Contains(v, want) {
			t.Fatalf("confirm missing %q:\n%s", want, v)
		}
	}
	key(m, "y")
	if m.hub.confirm != nil || m.hub.focus != hubJobs {
		t.Fatal("y should queue the pull and focus the downloads")
	}
	waitUntil(t, func() bool {
		jobs := f.pulls.Jobs()
		return len(jobs) == 1 && jobs[0].State == pull.Running && jobs[0].Done > 0
	})
	v = m.View()
	if !strings.Contains(v, "Downloads (1)") || !strings.Contains(v, "50%") || !strings.Contains(v, "██████████░░░░░░░░░░") || !strings.Contains(v, "4.1 GiB/8.1 GiB") {
		t.Fatalf("progress missing:\n%s", v)
	}
	f.mu.Lock()
	if len(f.pulled) != 1 || !strings.Contains(f.pulled[0], `"from":"ai/qwen3:8b-q8_0"`) {
		t.Fatalf("pulled = %v", f.pulled)
	}
	f.mu.Unlock()
	key(m, "c")
	waitUntil(t, func() bool { return f.pulls.Jobs()[0].State == pull.Cancelled })
	if !strings.Contains(m.hub.note, "Cancelled ai/qwen3:8b-q8_0") {
		t.Fatalf("note = %q", m.hub.note)
	}
	// esc leaves the downloads, then the screen, back to the model list.
	key(m, "esc")
	key(m, "esc")
	if m.screen != screenModels {
		t.Fatalf("screen = %v", m.screen)
	}
}

func TestHubRefusesWithoutSpace(t *testing.T) {
	f := newHubFixture(t, 9<<30)
	m := f.m
	f.open(t)
	key(m, "j")
	key(m, "enter")
	v := m.View()
	if !strings.Contains(v, "Not enough space: 1.1 GiB short.") || strings.Contains(v, "y queues") {
		t.Fatalf("should refuse:\n%s", v)
	}
	key(m, "y")
	if len(f.pulls.Jobs()) != 0 || m.hub.confirm == nil {
		t.Fatal("y must not queue a pull that does not fit")
	}
	key(m, "n")
	if m.hub.confirm != nil {
		t.Fatal("n closes the check")
	}
}

func TestHubOnlyOwnerCancels(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	if _, err := f.pulls.Enqueue("someone", "ai/other:latest", 1<<30); err != nil {
		t.Fatal(err)
	}
	f.open(t)
	key(m, "tab")
	if m.hub.focus != hubJobs {
		t.Fatalf("tab from tags should reach the downloads, focus %v", m.hub.focus)
	}
	key(m, "c")
	if !strings.Contains(m.hub.note, "Only someone can cancel") {
		t.Fatalf("note = %q", m.hub.note)
	}
}

func TestHubHiddenWhenOff(t *testing.T) {
	f := newHubFixture(t, 1)
	f.m.pulls = nil
	key(f.m, "p")
	if f.m.screen != screenModels {
		t.Fatal("p does nothing without the pull manager")
	}
	if strings.Contains(strings.Join(f.m.helpLabels(false), " "), "Docker Hub") {
		t.Fatal("help should not offer the hub")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHubEnterDoesNotConfirmPull(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	f.open(t)
	key(m, "enter") // opens the check for "latest"
	if m.hub.confirm == nil {
		t.Fatal("enter on a tag opens the space check")
	}
	key(m, "enter")
	key(m, "enter")
	if len(f.pulls.Jobs()) != 0 || m.hub.confirm == nil {
		t.Fatal("enter must not queue a pull; only y does")
	}
}

func TestHubMarksPulledInSessionAndShowsSize(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	f.open(t)
	key(m, "j")
	key(m, "enter")
	key(m, "y")
	waitUntil(t, func() bool { j := f.pulls.Jobs(); return len(j) == 1 && j[0].State == pull.Running })
	f.release <- struct{}{}
	waitUntil(t, func() bool { return f.pulls.Jobs()[0].State == pull.Done })
	v := m.View()
	if !strings.Contains(v, "8b-q8_0") || strings.Count(v, "✓ pulled") != 2 {
		t.Fatalf("both tags should be marked pulled:\n%s", v)
	}
	if !strings.Contains(v, "done      8.1 GiB") {
		t.Fatalf("finished pull should show its size:\n%s", v)
	}
}

func TestHubTagPaneBeforeEnter(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	key(m, "p")
	m.Update(m.fetchRepos()())
	if v := m.View(); !strings.Contains(v, "enter lists the tags") || strings.Contains(v, "Reading tags") {
		t.Fatalf("tag pane before enter:\n%s", v)
	}
}

func TestModelsEmptyMentionsHubAndClearsLoading(t *testing.T) {
	f := newHubFixture(t, 50<<30)
	m := f.m
	m.models = nil
	m.Update(m.startFetch()())
	m.Update(modelsMsg{gen: m.gen, models: nil})
	v := m.View()
	if strings.Contains(v, "Fetching models") {
		t.Fatalf("loading text should go once the list arrived:\n%s", v)
	}
	if !strings.Contains(v, "press p to search Docker Hub") {
		t.Fatalf("empty list should mention p:\n%s", v)
	}
}
