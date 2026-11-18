// Package gpu serializes model load and unload calls. Docker Model Runner
// uses one shared GPU, so two SSH sessions must not warm or unload at once.
package gpu

import (
	"context"
	"sync"
)

// Gate is a context-aware mutex. Do runs fn while holding the gate.
// If ctx is cancelled while waiting, Do returns ctx.Err() and does not run fn.
type Gate struct {
	mu sync.Mutex
}

// New returns a gate. The zero Gate is also usable.
func New() *Gate { return &Gate{} }

// Do holds the gate for the duration of fn.
func (g *Gate) Do(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	acquired := make(chan struct{})
	go func() {
		g.mu.Lock()
		close(acquired)
	}()
	select {
	case <-acquired:
		defer g.mu.Unlock()
		return fn(ctx)
	case <-ctx.Done():
		go func() {
			<-acquired
			g.mu.Unlock()
		}()
		return ctx.Err()
	}
}
