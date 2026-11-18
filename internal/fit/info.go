// Package fit estimates how much GPU or system memory a GGUF model needs at
// a given context size, and decides before loading whether it fits.
//
// The estimate mirrors how llama.cpp (the engine behind Docker Model Runner)
// allocates memory: weights, KV cache, compute buffers and a per-device
// runtime overhead. It is deliberately a little pessimistic.
package fit

import (
	"errors"
	"fmt"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/gguf"
)

// ModelInfo is what the estimator needs to know about a model.
type ModelInfo struct {
	Arch string
	// ContextLength is the trained context (n_ctx_train). Zero if unknown.
	ContextLength int
	Layers        int
	Embedding     int
	Vocab         int
	// HeadCountKV is the KV head count per layer. Zero means the layer
	// keeps no KV cache (recurrent or linear-attention layers).
	HeadCountKV []int
	KeyLength   int
	ValueLength int
	// SlidingWindow is the attention window of SWA layers, in tokens.
	SlidingWindow int
	// SWA marks layers that use the sliding window.
	SWA []bool
	// WeightBytes is the tensor data of all weight shards.
	WeightBytes int64
	// ProjectorBytes is the multimodal projector, if any.
	ProjectorBytes int64
}

// Known reports whether the KV geometry is complete.
func (m ModelInfo) Known() bool {
	return m.Layers > 0 && len(m.HeadCountKV) == m.Layers && m.KeyLength > 0 && m.ValueLength > 0 && m.WeightBytes > 0
}

// MaxContext is the trained context, or 0 when unknown.
func (m ModelInfo) MaxContext() int { return m.ContextLength }

// FromHeaders builds ModelInfo from the GGUF headers of one model. Weight
// shards and projectors are told apart by architecture: projectors are
// "clip" (or carry clip.* keys).
func FromHeaders(headers []gguf.Header) (ModelInfo, error) {
	var info ModelInfo
	var main *gguf.Header
	for i := range headers {
		h := &headers[i]
		if isProjector(h.Metadata) {
			info.ProjectorBytes += h.FileSize
			continue
		}
		info.WeightBytes += h.DataSize()
		arch, _ := gguf.String(h.Metadata, "general.architecture")
		if arch == "" {
			continue
		}
		if _, ok := gguf.Uint(h.Metadata, arch+".block_count"); ok && main == nil {
			main = h
		}
	}
	if main == nil {
		return info, errors.New("no GGUF header with model architecture and block count")
	}
	md := main.Metadata
	arch, _ := gguf.String(md, "general.architecture")
	info.Arch = arch
	key := func(k string) string { return arch + "." + k }
	u := func(k string) int {
		v, _ := gguf.Uint(md, key(k))
		return int(v)
	}
	info.Layers = u("block_count")
	info.ContextLength = u("context_length")
	info.Embedding = u("embedding_length")
	info.Vocab = u("vocab_size")
	if info.Vocab == 0 {
		info.Vocab = int(main.ArrayLen["tokenizer.ggml.tokens"])
	}
	heads := u("attention.head_count")
	info.KeyLength = u("attention.key_length")
	info.ValueLength = u("attention.value_length")
	if info.KeyLength == 0 && heads > 0 && info.Embedding > 0 {
		info.KeyLength = info.Embedding / heads
	}
	if info.ValueLength == 0 {
		info.ValueLength = info.KeyLength
	}
	// MLA models (DeepSeek-style) cache a compressed latent, not full heads.
	if lora, ok := gguf.Uint(md, key("attention.kv_lora_rank")); ok && lora > 0 {
		rope, _ := gguf.Uint(md, key("rope.dimension_count"))
		info.KeyLength = int(lora + rope)
		info.ValueLength = int(lora)
	}

	info.HeadCountKV = perLayer(md, key("attention.head_count_kv"), info.Layers, heads)
	if info.KeyLength == 0 && info.Layers > 0 {
		// Models without attention metadata (pure recurrent) keep no KV.
		info.HeadCountKV = make([]int, info.Layers)
		info.KeyLength, info.ValueLength = 1, 1
	}
	if n, ok := gguf.Uint(md, key("full_attention_interval")); ok && n > 1 {
		// Hybrid models: only every n-th layer is full attention with a
		// KV cache; the others are linear attention with a small state.
		for i := range info.HeadCountKV {
			if (i+1)%int(n) != 0 {
				info.HeadCountKV[i] = 0
			}
		}
	}
	info.SlidingWindow = u("attention.sliding_window")
	info.SWA = swaLayers(md, arch, info.Layers, info.SlidingWindow)
	return info, nil
}

func isProjector(md map[string]any) bool {
	arch, _ := gguf.String(md, "general.architecture")
	if arch == "clip" || arch == "mmproj" {
		return true
	}
	for k := range md {
		if strings.HasPrefix(k, "clip.") {
			return true
		}
	}
	return false
}

// perLayer expands a scalar or per-layer array to n entries. A missing key
// falls back to def.
func perLayer(md map[string]any, k string, n, def int) []int {
	out := make([]int, n)
	vals, ok := gguf.Ints(md, k)
	for i := range out {
		switch {
		case !ok || len(vals) == 0:
			out[i] = def
		case len(vals) == 1:
			out[i] = int(vals[0])
		case i < len(vals):
			out[i] = int(vals[i])
		}
	}
	return out
}

// swaLayers marks sliding-window layers. Explicit GGUF patterns win. Some
// architectures fix the pattern in llama.cpp itself; anything else with a
// window but no pattern is treated as full attention, which overestimates.
func swaLayers(md map[string]any, arch string, n, window int) []bool {
	out := make([]bool, n)
	if window <= 0 {
		return out
	}
	pk := arch + ".attention.sliding_window_pattern"
	if b, ok := gguf.Bools(md, pk); ok && len(b) > 0 {
		for i := range out {
			out[i] = b[i%len(b)]
		}
		return out
	}
	period := 0
	if p, ok := gguf.Uint(md, pk); ok && p > 1 {
		period = int(p)
	} else {
		switch arch {
		case "gpt-oss", "gemma2", "cohere2":
			period = 2
		case "gemma3", "gemma3n":
			period = 6
		}
	}
	if period > 1 {
		// Every period-th layer is global; the others slide.
		for i := range out {
			out[i] = (i+1)%period != 0
		}
	}
	return out
}

// Summary is a short description for the UI.
func (m ModelInfo) Summary() string {
	parts := []string{}
	if m.ContextLength > 0 {
		parts = append(parts, "max ctx "+FormatTokens(m.ContextLength))
	}
	if m.WeightBytes > 0 {
		parts = append(parts, "weights "+FormatBytes(m.WeightBytes))
	}
	if m.ProjectorBytes > 0 {
		parts = append(parts, "vision "+FormatBytes(m.ProjectorBytes))
	}
	if m.Arch != "" {
		parts = append(parts, m.Arch)
	}
	return strings.Join(parts, " · ")
}

// FormatTokens prints 32768 as "32k".
func FormatTokens(n int) string {
	if n >= 1024 && n%1024 == 0 {
		return fmt.Sprintf("%dk", n/1024)
	}
	return fmt.Sprintf("%d", n)
}

// FormatBytes prints GiB with one decimal, or MiB below 1 GiB.
func FormatBytes(n int64) string {
	const gib = 1 << 30
	if n >= gib || n <= -gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%d MiB", n>>20)
}
