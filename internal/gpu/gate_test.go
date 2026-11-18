package gpu

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestGateSerializes(t *testing.T) {
	g := New()
	var mu sync.Mutex
	in := 0
	max := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := g.Do(context.Background(), func(context.Context) error {
				mu.Lock()
				in++
				if in > max {
					max = in
				}
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
				mu.Lock()
				in--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("do: %v", err)
			}
		}()
	}
	wg.Wait()
	if max != 1 {
		t.Fatalf("max concurrent = %d", max)
	}
}

func TestGateCancelWhileWaiting(t *testing.T) {
	g := New()
	started := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- g.Do(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := g.Do(ctx, func(context.Context) error {
		t.Fatal("fn ran after cancel")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}

	if err := g.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("gate stuck: %v", err)
	}
}
