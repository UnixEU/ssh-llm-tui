package session

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/gpu"
)

func TestLeaveSkipsUnloadWhileAnotherSessionIsConnected(t *testing.T) {
	var mu sync.Mutex
	var calls int
	tr := New(Options{
		Timeout: time.Second,
		Log:     log.New(io.Discard, "", 0),
		Unload: func(context.Context) (int, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return 1, nil
		},
	})
	tr.Enter()
	tr.Enter()
	tr.Leave()
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 0 {
		t.Fatalf("unload calls = %d, want 0 while a session remains", got)
	}
	if tr.Count() != 1 {
		t.Fatalf("count = %d", tr.Count())
	}
}

func TestLastSessionUnloads(t *testing.T) {
	done := make(chan int, 1)
	tr := New(Options{
		Timeout: time.Second,
		Log:     log.New(io.Discard, "", 0),
		Unload: func(context.Context) (int, error) {
			done <- 3
			return 3, nil
		},
	})
	tr.Enter()
	tr.Leave()
	select {
	case n := <-done:
		if n != 3 {
			t.Fatalf("unloaded %d", n)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unload")
	}
	if tr.Count() != 0 {
		t.Fatalf("count = %d", tr.Count())
	}
}

func TestUnloadSkippedWhenSessionConnectsWhileWaitingForGPU(t *testing.T) {
	gate := gpu.New()
	holdDone := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = gate.Do(context.Background(), func(context.Context) error {
			close(holdDone)
			<-release
			return nil
		})
	}()
	<-holdDone

	var calls int
	tr := New(Options{
		Gate:    gate,
		Timeout: time.Second,
		Log:     log.New(io.Discard, "", 0),
		Unload: func(context.Context) (int, error) {
			calls++
			return 1, nil
		},
	})
	tr.Enter()
	tr.Leave()
	// The unload is blocked on the GPU lock. A new session connects first.
	time.Sleep(20 * time.Millisecond)
	tr.Enter()
	close(release)

	time.Sleep(50 * time.Millisecond)
	if calls != 0 {
		t.Fatalf("unload calls = %d, want 0 after a new session connected", calls)
	}
}

func TestUnloadTimeoutIsLogged(t *testing.T) {
	var buf syncBuffer
	tr := New(Options{
		Timeout: 40 * time.Millisecond,
		Log:     log.New(&buf, "", 0),
		Unload: func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	})
	tr.Enter()
	tr.Leave()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if buf.Contains("timed out") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log = %q", buf.String())
}

func TestUnloadIfSoleSkipsWhenOthersConnected(t *testing.T) {
	var calls int
	tr := New(Options{
		Timeout: time.Second,
		Log:     log.New(io.Discard, "", 0),
		Unload: func(context.Context) (int, error) {
			calls++
			return 1, nil
		},
	})
	tr.Enter()
	tr.Enter()
	n, skipped, err := tr.UnloadIfSole(context.Background())
	if err != nil || !skipped || n != 0 || calls != 0 {
		t.Fatalf("n=%d skipped=%v err=%v calls=%d", n, skipped, err, calls)
	}
}

func TestUnloadIfSoleUnloadsTheOnlySession(t *testing.T) {
	tr := New(Options{
		Timeout: time.Second,
		Log:     log.New(io.Discard, "", 0),
		Unload: func(context.Context) (int, error) {
			return 2, nil
		},
	})
	tr.Enter()
	n, skipped, err := tr.UnloadIfSole(context.Background())
	if err != nil || skipped || n != 2 {
		t.Fatalf("n=%d skipped=%v err=%v", n, skipped, err)
	}
}

func TestUnloadErrorIsLogged(t *testing.T) {
	var buf syncBuffer
	tr := New(Options{
		Timeout: time.Second,
		Log:     log.New(&buf, "", 0),
		Unload: func(context.Context) (int, error) {
			return 0, errors.New("runner busy")
		},
	})
	tr.Enter()
	tr.Leave()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if buf.Contains("runner busy") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log = %q", buf.String())
}

type syncBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.b)
}

func (s *syncBuffer) Contains(sub string) bool {
	return len(sub) > 0 && len(s.String()) > 0 && contains(s.String(), sub)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || index(s, sub) >= 0)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
