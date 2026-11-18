package fit

import (
	"errors"
	"fmt"
	"sync"

	"github.com/UnixEU/ssh-llm-tui/internal/gguf"
	"github.com/UnixEU/ssh-llm-tui/internal/modelstore"
)

// ErrNoStore means the DMR model store is not mounted, so model headers
// cannot be read.
var ErrNoStore = errors.New("model store not mounted")

// Resolver reads model metadata from the DMR model store and caches it by
// store id (model blobs are content addressed, so the cache never goes stale).
type Resolver struct {
	store *modelstore.Store
	mu    sync.Mutex
	cache map[string]ModelInfo
}

// NewResolver wraps a store. A nil store makes every lookup ErrNoStore.
func NewResolver(store *modelstore.Store) *Resolver {
	return &Resolver{store: store, cache: map[string]ModelInfo{}}
}

// Info returns metadata for a model reference as DMR lists it.
func (r *Resolver) Info(model string) (ModelInfo, error) {
	if r == nil || r.store == nil {
		return ModelInfo{}, ErrNoStore
	}
	files, err := r.store.Lookup(model)
	if err != nil {
		return ModelInfo{}, err
	}
	r.mu.Lock()
	if info, ok := r.cache[files.ID]; ok {
		r.mu.Unlock()
		return info, nil
	}
	r.mu.Unlock()

	headers := make([]gguf.Header, 0, len(files.Weights)+len(files.Projectors))
	for _, p := range append(append([]string{}, files.Weights...), files.Projectors...) {
		h, err := gguf.ReadFile(p)
		if err != nil {
			return ModelInfo{}, fmt.Errorf("read model header: %w", err)
		}
		headers = append(headers, h)
	}
	info, err := FromHeaders(headers)
	if err != nil {
		return ModelInfo{}, err
	}
	r.mu.Lock()
	r.cache[files.ID] = info
	r.mu.Unlock()
	return info, nil
}
