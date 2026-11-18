// Package session counts live SSH sessions and unloads Docker Model Runner
// when the last one ends.
package session

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/gpu"
)

// UnloadFunc stops every idle DMR runner. It is client.UnloadAll in production.
type UnloadFunc func(ctx context.Context) (int, error)

// Tracker counts active TUI sessions. Leave unloads models only when the
// count hits zero, and it re-checks after taking the GPU lock so a session
// that connected while unload was waiting is left alone.
type Tracker struct {
	mu         sync.Mutex
	n          int
	generation int
	gate       *gpu.Gate
	unload     UnloadFunc
	timeout    time.Duration
	log        *log.Logger
}

// Options configures a Tracker.
type Options struct {
	Gate    *gpu.Gate
	Unload  UnloadFunc
	Timeout time.Duration
	Log     *log.Logger
}

// New returns a tracker. A nil logger discards messages. Timeout defaults to 15s.
func New(opts Options) *Tracker {
	if opts.Gate == nil {
		opts.Gate = gpu.New()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	logger := opts.Log
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Tracker{
		gate:    opts.Gate,
		unload:  opts.Unload,
		timeout: opts.Timeout,
		log:     logger,
	}
}

// Count is the number of sessions currently inside the TUI.
func (t *Tracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

// Enter records a new session.
func (t *Tracker) Enter() {
	t.mu.Lock()
	t.n++
	t.generation++
	t.mu.Unlock()
}

// UnloadIfSole unloads every runner when at most one TUI session is
// connected (the caller, who stays connected). Extra sessions skip the
// unload. Esc on the chat screen uses this; disconnect uses Leave.
func (t *Tracker) UnloadIfSole(ctx context.Context) (int, bool, error) {
	t.mu.Lock()
	n := t.n
	t.mu.Unlock()
	if n > 1 {
		t.log.Printf("not unloading; %d other session(s) still connected", n-1)
		return 0, true, nil
	}
	var unloaded int
	err := t.gate.Do(ctx, func(ctx context.Context) error {
		t.mu.Lock()
		busy := t.n > 1
		t.mu.Unlock()
		if busy {
			return errSkipped
		}
		if t.unload == nil {
			t.log.Printf("unload skipped; no DMR client")
			return nil
		}
		var uerr error
		unloaded, uerr = t.unload(ctx)
		if uerr != nil {
			return uerr
		}
		t.log.Printf("unloaded %d DMR runner(s) for the only connected session", unloaded)
		return nil
	})
	if errors.Is(err, errSkipped) {
		t.log.Printf("unload cancelled; another session connected")
		return 0, true, nil
	}
	return unloaded, false, err
}

// Leave records a session ending (clean exit or a dropped connection).
// When it was the last session, unload runs in the background.
func (t *Tracker) Leave() {
	t.mu.Lock()
	if t.n > 0 {
		t.n--
	}
	others := t.n
	gen := t.generation
	t.mu.Unlock()
	if others > 0 {
		t.log.Printf("session ended; %d session(s) still connected, not unloading models", others)
		return
	}
	t.log.Printf("last session ended; unloading all DMR runners")
	go t.unloadIfIdle(gen)
}

func (t *Tracker) unloadIfIdle(gen int) {
	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()
	err := t.gate.Do(ctx, func(ctx context.Context) error {
		t.mu.Lock()
		busy := t.n > 0 || t.generation != gen
		t.mu.Unlock()
		if busy {
			return errSkipped
		}
		if t.unload == nil {
			t.log.Printf("unload skipped; no DMR client")
			return nil
		}
		n, err := t.unload(ctx)
		if err != nil {
			return err
		}
		t.log.Printf("unloaded %d DMR runner(s) after the last session ended", n)
		return nil
	})
	if err == nil {
		return
	}
	if errors.Is(err, errSkipped) {
		t.log.Printf("unload cancelled; a session connected before the GPU lock was free")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.log.Printf("unload timed out after %s: %v", t.timeout, err)
		return
	}
	t.log.Printf("unload failed: %v", err)
}

var errSkipped = errors.New("session connected")
