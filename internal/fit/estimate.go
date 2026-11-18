package fit

const (
	mib = int64(1) << 20
	gib = int64(1) << 30

	// DefaultUBatch is llama.cpp's default physical batch (n_ubatch).
	DefaultUBatch = 512
	// deviceOverhead is the CUDA context and runtime buffers per GPU.
	deviceOverhead = 384 * mib
	// computeBase is the fixed part of the compute buffer.
	computeBase = 256 * mib
	// headerUncertainty pads estimates built from GGUF headers.
	headerUncertainty = 1.05
)

// Estimate is the memory a model needs at one context size.
type Estimate struct {
	Context   int
	Weights   int64
	KV        int64
	Compute   int64
	Projector int64
	// Overhead is per device used (each GPU, or once for CPU).
	Overhead int64
	// Factor is the uncertainty multiplier applied by Total.
	Factor float64
	// Measured is true when the figure comes from an earlier load.
	Measured bool
}

// KVBytesPerElem is the KV cache element size for a llama.cpp cache type.
func KVBytesPerElem(cacheType string) float64 {
	switch cacheType {
	case "f32":
		return 4
	case "q8_0":
		return 34.0 / 32
	case "q5_0", "q5_1":
		return 24.0 / 32
	case "q4_0", "q4_1", "iq4_nl":
		return 18.0 / 32
	default: // f16, bf16
		return 2
	}
}

// KVBytes is the KV cache size for ctx tokens. SWA layers keep at most
// window + ubatch cells, as llama.cpp sizes its SWA cache.
func KVBytes(m ModelInfo, ctx int, bytesPerElem float64) int64 {
	if ctx <= 0 || !m.Known() {
		return 0
	}
	var total float64
	for i := 0; i < m.Layers; i++ {
		heads := m.HeadCountKV[i]
		if heads <= 0 {
			continue
		}
		cells := ctx
		if i < len(m.SWA) && m.SWA[i] && m.SlidingWindow > 0 {
			if w := m.SlidingWindow + DefaultUBatch; w < cells {
				cells = w
			}
		}
		perToken := float64(heads) * float64(m.KeyLength+m.ValueLength) * bytesPerElem
		total += perToken * float64(cells)
	}
	return int64(total)
}

// ComputeBytes approximates llama.cpp's compute buffer: logits and
// activations for one ubatch plus a fixed floor.
func ComputeBytes(m ModelInfo) int64 {
	vocab := int64(m.Vocab)
	if vocab <= 0 {
		vocab = 128000
	}
	embd := int64(m.Embedding)
	if embd <= 0 {
		embd = 4096
	}
	return computeBase + 4*DefaultUBatch*(vocab+8*embd)
}

// EstimateFor builds an estimate from model metadata.
func EstimateFor(m ModelInfo, ctx int, cacheType string) Estimate {
	return Estimate{
		Context:   ctx,
		Weights:   m.WeightBytes,
		KV:        KVBytes(m, ctx, KVBytesPerElem(cacheType)),
		Compute:   ComputeBytes(m),
		Projector: m.ProjectorBytes,
		Overhead:  deviceOverhead,
		Factor:    headerUncertainty,
	}
}

// Model is weights + KV + compute + projector, padded by Factor, without
// per-device overhead.
func (e Estimate) Model() int64 {
	f := e.Factor
	if f <= 0 {
		f = 1
	}
	return int64(float64(e.Weights+e.KV+e.Compute+e.Projector) * f)
}

// Total is Model plus overhead for n devices.
func (e Estimate) Total(devices int) int64 {
	if devices < 1 {
		devices = 1
	}
	return e.Model() + int64(devices)*e.Overhead
}
