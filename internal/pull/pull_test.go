package pull

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
)

type fakePuller struct {
	mu      sync.Mutex
	running map[string]bool
	max     int
	gates   map[string]chan error
}

func newFake() *fakePuller {
	return &fakePuller{running: map[string]bool{}, gates: map[string]chan error{}}
}

func (f *fakePuller) gate(ref string) chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.gates[ref]
	if !ok {
		g = make(chan error, 1)
		f.gates[ref] = g
	}
	return g
}

func (f *fakePuller) Pull(ctx context.Context, ref string, fn func(dmr.PullProgress)) error {
	f.mu.Lock()
	f.running[ref] = true
	if len(f.running) > f.max {
		f.max = len(f.running)
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.running, ref)
		f.mu.Unlock()
	}()
	fn(dmr.PullProgress{Done: 5, Total: 10})
	select {
	case err := <-f.gate(ref):
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func state(m *Manager, id int) Job {
	for _, j := range m.Jobs() {
		if j.ID == id {
			return j
		}
	}
	return Job{}
}

func TestQueueRunsInOrderWithLimit(t *testing.T) {
	f := newFake()
	m := New(context.Background(), f, 1)
	var finished []string
	var mu sync.Mutex
	m.OnFinish(func(j Job) { mu.Lock(); finished = append(finished, j.Ref+":"+j.State.String()); mu.Unlock() })
	a, _ := m.Enqueue("ann", "ai/a", 10)
	b, _ := m.Enqueue("bob", "ai/b", 30)
	if _, err := m.Enqueue("bob", "ai/a", 10); !errors.Is(err, ErrQueued) {
		t.Fatalf("duplicate: %v", err)
	}
	waitFor(t, func() bool { return state(m, a.ID).State == Running && state(m, a.ID).Done == 5 })
	if state(m, b.ID).State != Queued {
		t.Fatal("second pull should wait for the slot")
	}
	if p := m.Pending(); p != 5+30 {
		t.Fatalf("pending = %d", p)
	}
	f.gate("ai/a") <- nil
	waitFor(t, func() bool { return state(m, b.ID).State == Running })
	f.gate("ai/b") <- errors.New("boom")
	waitFor(t, func() bool { return state(m, b.ID).State == Failed })
	if j := state(m, a.ID); j.State != Done || j.Fraction() != 1 || j.Done != 10 {
		t.Fatalf("a = %+v", j)
	}
	if state(m, b.ID).Err != "boom" || m.Finished() != 2 || f.max != 1 {
		t.Fatalf("b = %+v finished %d max %d", state(m, b.ID), m.Finished(), f.max)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(finished) != 2 || finished[0] != "ai/a:done" || finished[1] != "ai/b:failed" {
		t.Fatalf("finished = %v", finished)
	}
}

func TestCancelRunningAndQueued(t *testing.T) {
	f := newFake()
	m := New(context.Background(), f, 1)
	a, _ := m.Enqueue("ann", "ai/a", 10)
	b, _ := m.Enqueue("ann", "ai/b", 10)
	waitFor(t, func() bool { return state(m, a.ID).State == Running })
	if !m.Cancel(b.ID) {
		t.Fatal("cancel queued")
	}
	waitFor(t, func() bool { return state(m, b.ID).State == Cancelled })
	if !m.Cancel(a.ID) {
		t.Fatal("cancel running")
	}
	waitFor(t, func() bool { return state(m, a.ID).State == Cancelled })
	if m.Cancel(a.ID) {
		t.Fatal("cancelling a finished job should report false")
	}
	if m.Pending() != 0 {
		t.Fatal("nothing pending after cancel")
	}
	// The same model can be queued again once the old job finished.
	if _, err := m.Enqueue("ann", "ai/a", 10); err != nil {
		t.Fatal(err)
	}
}

func TestFinishedJobsExpire(t *testing.T) {
	f := newFake()
	m := New(context.Background(), f, 2)
	now := time.Now()
	m.now = func() time.Time { return now }
	a, _ := m.Enqueue("ann", "ai/a", 10)
	waitFor(t, func() bool { return state(m, a.ID).State == Running })
	f.gate("ai/a") <- nil
	waitFor(t, func() bool { return state(m, a.ID).State == Done })
	now = now.Add(time.Hour)
	if len(m.Jobs()) != 0 {
		t.Fatal("finished jobs should drop off the list")
	}
}

func TestSpace(t *testing.T) {
	s := Space{Known: true, Free: 10 << 30, Size: 5 << 30, Pending: 2 << 30, Reserve: 2 << 30}
	if !s.OK() || s.Need() != 9<<30 {
		t.Fatalf("need %d", s.Need())
	}
	s.Pending = 4 << 30
	if s.OK() {
		t.Fatal("11 GiB needed, 10 free: refuse")
	}
	if !(Space{Size: 1 << 40}).OK() {
		t.Fatal("unknown disk is left to the caller")
	}
}
