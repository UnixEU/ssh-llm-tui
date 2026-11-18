package fit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Usage is GPU memory measured after a successful load.
type Usage struct {
	// PerGPU is the increase in used memory per GPU index, in bytes.
	PerGPU map[int]int64 `json:"per_gpu"`
	At     time.Time     `json:"at"`
}

// Total is the sum over GPUs.
func (u Usage) Total() int64 {
	var n int64
	for _, v := range u.PerGPU {
		n += v
	}
	return n
}

// Learned remembers measured usage per (model, context, target). It is the
// fallback when a model's GGUF header cannot be read. Writes go to a small
// JSON file when a path is set; failures keep the data in memory only.
type Learned struct {
	path string
	mu   sync.Mutex
	data map[string]Usage
}

// minLearned ignores deltas too small to be a model load (the model was
// already resident, or another process freed memory at the same time).
const minLearned = 256 * mib

// LoadLearned reads path if it exists. An empty path keeps data in memory.
func LoadLearned(path string) *Learned {
	l := &Learned{path: path, data: map[string]Usage{}}
	if path == "" {
		return l
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	_ = json.Unmarshal(raw, &l.data)
	if l.data == nil {
		l.data = map[string]Usage{}
	}
	return l
}

// LearnedKey identifies one measured configuration.
func LearnedKey(model string, ctx int, t Target) string {
	return model + "|" + strconv.Itoa(ctx) + "|" + t.Key()
}

// Get returns a measurement.
func (l *Learned) Get(model string, ctx int, t Target) (Usage, bool) {
	if l == nil {
		return Usage{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	u, ok := l.data[LearnedKey(model, ctx, t)]
	return u, ok
}

// Record stores a measurement and persists it. It returns false when the
// delta is too small to be meaningful.
func (l *Learned) Record(model string, ctx int, t Target, perGPU map[int]int64) (bool, error) {
	if l == nil || t.Kind == TargetCPU {
		return false, nil
	}
	u := Usage{PerGPU: map[int]int64{}, At: time.Now().UTC()}
	for k, v := range perGPU {
		if v > 0 {
			u.PerGPU[k] = v
		}
	}
	if u.Total() < minLearned {
		return false, nil
	}
	l.mu.Lock()
	l.data[LearnedKey(model, ctx, t)] = u
	snapshot, _ := json.MarshalIndent(l.data, "", "  ")
	l.mu.Unlock()
	if l.path == "" {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return true, err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, snapshot, 0o600); err != nil {
		return true, err
	}
	return true, os.Rename(tmp, l.path)
}

// EstimateFromUsage turns a measurement into an estimate. The measured
// figure already includes compute buffers and per-device overhead.
func EstimateFromUsage(u Usage, ctx int) Estimate {
	devices := int64(len(u.PerGPU))
	if devices < 1 {
		devices = 1
	}
	weights := u.Total() - devices*deviceOverhead
	if weights < 0 {
		weights = 0
	}
	return Estimate{
		Context:  ctx,
		Weights:  weights,
		Overhead: deviceOverhead,
		Factor:   1.0,
		Measured: true,
	}
}
