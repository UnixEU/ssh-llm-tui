package fit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/gguf"
)

func header(t *testing.T, kvs []gguf.KV, data int64) gguf.Header {
	t.Helper()
	var buf bytes.Buffer
	if err := gguf.Write(&buf, kvs, []string{"w"}, 0); err != nil {
		t.Fatal(err)
	}
	h, err := gguf.Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// Tensor data is not written; only its size matters here.
	h.FileSize = h.DataOffset + data
	return h
}

// Llama 3.x 8B geometry: 32 layers, 32 query heads, 8 KV heads, head dim 128.
func llama8B(t *testing.T) ModelInfo {
	h := header(t, []gguf.KV{
		kv("general.architecture", "llama"),
		kv("llama.block_count", uint32(32)),
		kv("llama.context_length", uint32(131072)),
		kv("llama.embedding_length", uint32(4096)),
		kv("llama.attention.head_count", uint32(32)),
		kv("llama.attention.head_count_kv", uint32(8)),
		kv("llama.vocab_size", uint32(128256)),
	}, 4_920_000_000)
	info, err := FromHeaders([]gguf.Header{h})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// gpt-oss 20B geometry: 24 layers, 8 KV heads of 64, window 128, every
// other layer sliding.
func gptOSS20B(t *testing.T) ModelInfo {
	h := header(t, []gguf.KV{
		kv("general.architecture", "gpt-oss"),
		kv("gpt-oss.block_count", uint32(24)),
		kv("gpt-oss.context_length", uint32(131072)),
		kv("gpt-oss.embedding_length", uint32(2880)),
		kv("gpt-oss.attention.head_count", uint32(64)),
		kv("gpt-oss.attention.head_count_kv", uint32(8)),
		kv("gpt-oss.attention.key_length", uint32(64)),
		kv("gpt-oss.attention.value_length", uint32(64)),
		kv("gpt-oss.attention.sliding_window", uint32(128)),
	}, 12_100_000_000)
	info, err := FromHeaders([]gguf.Header{h})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestDenseKVMatchesHandCalculation(t *testing.T) {
	m := llama8B(t)
	if m.KeyLength != 128 || m.ValueLength != 128 {
		t.Fatalf("head dim %d/%d", m.KeyLength, m.ValueLength)
	}
	// 2 (K+V) * 32 layers * 8 heads * 128 dim * 2 bytes = 128 KiB per token.
	if got := KVBytes(m, 32768, 2); got != 4*gib {
		t.Fatalf("KV at 32k = %d, want 4 GiB", got)
	}
	if got := KVBytes(m, 131072, 2); got != 16*gib {
		t.Fatalf("KV at 128k = %d, want 16 GiB", got)
	}
	if got := KVBytes(m, 32768, KVBytesPerElem("q8_0")); got >= 4*gib/3*2 || got <= 2*gib {
		t.Fatalf("q8_0 KV at 32k = %d, want about 2.1 GiB", got)
	}
}

func TestSlidingWindowLayersAreCapped(t *testing.T) {
	m := gptOSS20B(t)
	swa := 0
	for _, s := range m.SWA {
		if s {
			swa++
		}
	}
	if swa != 12 {
		t.Fatalf("swa layers = %d, want 12", swa)
	}
	perTokenLayer := int64(8 * (64 + 64) * 2)
	full := 12 * perTokenLayer * 131072
	sliding := 12 * perTokenLayer * (128 + DefaultUBatch)
	if got := KVBytes(m, 131072, 2); got != full+sliding {
		t.Fatalf("KV = %d, want %d", got, full+sliding)
	}
	if got := KVBytes(m, 131072, 2); got > 3*gib+32*mib {
		t.Fatalf("KV at 128k should be about 3 GiB, got %s", FormatBytes(got))
	}
}

func TestExplicitPatternAndPerLayerHeads(t *testing.T) {
	h := header(t, []gguf.KV{
		kv("general.architecture", "gemma3"),
		kv("gemma3.block_count", uint32(62)),
		kv("gemma3.context_length", uint32(131072)),
		kv("gemma3.embedding_length", uint32(5376)),
		kv("gemma3.attention.head_count", uint32(32)),
		kv("gemma3.attention.head_count_kv", uint32(16)),
		kv("gemma3.attention.key_length", uint32(128)),
		kv("gemma3.attention.value_length", uint32(128)),
		kv("gemma3.attention.sliding_window", uint32(1024)),
	}, 1000)
	m, err := FromHeaders([]gguf.Header{h})
	if err != nil {
		t.Fatal(err)
	}
	global := 0
	for _, s := range m.SWA {
		if !s {
			global++
		}
	}
	if global != 10 {
		t.Fatalf("global layers = %d, want 10 (every 6th of 62)", global)
	}
	perTokenLayer := int64(16 * 256 * 2)
	want := 10*perTokenLayer*32768 + 52*perTokenLayer*(1024+DefaultUBatch)
	if got := KVBytes(m, 32768, 2); got != want {
		t.Fatalf("KV = %d, want %d", got, want)
	}

	// Per-layer array with zeros: those layers keep no KV.
	h2 := header(t, []gguf.KV{
		kv("general.architecture", "hybrid"),
		kv("hybrid.block_count", uint32(4)),
		kv("hybrid.attention.head_count", uint32(8)),
		kv("hybrid.attention.head_count_kv", []int32{0, 4, 0, 4}),
		kv("hybrid.attention.key_length", uint32(64)),
	}, 1000)
	m2, err := FromHeaders([]gguf.Header{h2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := KVBytes(m2, 1024, 2), int64(2*4*128*2*1024); got != want {
		t.Fatalf("hybrid KV = %d, want %d", got, want)
	}
}

func TestFullAttentionInterval(t *testing.T) {
	h := header(t, []gguf.KV{
		kv("general.architecture", "qwen3next"),
		kv("qwen3next.block_count", uint32(48)),
		kv("qwen3next.full_attention_interval", uint32(4)),
		kv("qwen3next.attention.head_count", uint32(16)),
		kv("qwen3next.attention.head_count_kv", uint32(2)),
		kv("qwen3next.attention.key_length", uint32(256)),
	}, 1000)
	m, err := FromHeaders([]gguf.Header{h})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := KVBytes(m, 8192, 2), int64(12*2*512*2*8192); got != want {
		t.Fatalf("KV = %d, want %d", got, want)
	}
}

func TestProjectorIsSeparated(t *testing.T) {
	w := header(t, []gguf.KV{
		kv("general.architecture", "llama"),
		kv("llama.block_count", uint32(2)),
		kv("llama.attention.head_count", uint32(2)),
		kv("llama.embedding_length", uint32(128)),
	}, 10_000)
	p := header(t, []gguf.KV{kv("general.architecture", "clip")}, 5_000)
	m, err := FromHeaders([]gguf.Header{w, p})
	if err != nil {
		t.Fatal(err)
	}
	if m.WeightBytes != 10_000 || m.ProjectorBytes < 5_000 {
		t.Fatalf("weights %d projector %d", m.WeightBytes, m.ProjectorBytes)
	}
	if _, err := FromHeaders([]gguf.Header{p}); err == nil {
		t.Fatal("projector alone accepted as a model")
	}
}

func TestCapContexts(t *testing.T) {
	choices := []int{8192, 16384, 32768, 65536, 131072}
	if got := CapContexts(choices, 32768); len(got) != 3 || got[2] != 32768 {
		t.Fatalf("cap 32k: %v", got)
	}
	if got := CapContexts(choices, 0); len(got) != 5 {
		t.Fatalf("unknown max: %v", got)
	}
	if got := CapContexts(choices, 4096); len(got) != 1 || got[0] != 4096 {
		t.Fatalf("small model: %v", got)
	}
	parsed, err := ParseContexts("8k, 16384,32K")
	if err != nil || len(parsed) != 3 || parsed[0] != 8192 || parsed[2] != 32768 {
		t.Fatalf("parse: %v %v", parsed, err)
	}
	if _, err := ParseContexts("8k,abc"); err == nil {
		t.Fatal("bad context accepted")
	}
}

// Two 16 GiB-class GPUs as nvidia-smi reports them when idle.
func twoGPUs() []Device {
	return []Device{
		{Index: 0, FreeBytes: 15998 * mib, TotalBytes: 16380 * mib},
		{Index: 1, FreeBytes: 15998 * mib, TotalBytes: 16380 * mib},
	}
}

var defaultMargins = Margins{MinBytes: gib, Percent: 5}

func TestMarginIsMaxOfFloorAndPercent(t *testing.T) {
	if got := defaultMargins.For(16380 * mib); got != gib {
		t.Fatalf("16 GiB card margin %d", got)
	}
	if got := defaultMargins.For(80 * gib); got != 4*gib {
		t.Fatalf("80 GiB card margin %s", FormatBytes(got))
	}
}

func TestSplitAcrossTwoGPUs(t *testing.T) {
	m := ModelInfo{
		Layers: 64, HeadCountKV: make([]int, 64), KeyLength: 128, ValueLength: 128,
		Embedding: 5120, Vocab: 151936, WeightBytes: 16_200_000_000, ContextLength: 131072,
	}
	for i := range m.HeadCountKV {
		m.HeadCountKV[i] = 4
	}
	est := EstimateFor(m, 32768, "f16")
	auto := Check(est, Target{Kind: TargetAuto}, twoGPUs(), Device{}, defaultMargins)
	if auto.Verdict != Fits {
		t.Fatalf("auto verdict %v, shares %+v", auto.Verdict, auto.Shares)
	}
	if len(auto.Shares) != 2 || auto.Shares[0].Need != auto.Shares[1].Need {
		t.Fatalf("equal GPUs should get equal shares: %+v", auto.Shares)
	}
	one := Check(est, Target{Kind: TargetGPU, GPU: 1}, twoGPUs(), Device{}, defaultMargins)
	if one.Verdict != NoFit {
		t.Fatalf("single GPU verdict %v, need %s", one.Verdict, FormatBytes(one.Need()))
	}
	big := Check(EstimateFor(m, 131072, "f16"), Target{Kind: TargetAuto}, twoGPUs(), Device{}, defaultMargins)
	if big.Verdict != NoFit {
		t.Fatalf("128k verdict %v, need %s", big.Verdict, FormatBytes(big.Need()))
	}
	best, ok := LargestFitting(m, []int{8192, 16384, 32768, 65536, 131072}, "f16", Target{Kind: TargetAuto}, twoGPUs(), Device{}, defaultMargins, false)
	if !ok || best != 65536 {
		t.Fatalf("largest fitting = %d %v", best, ok)
	}
}

func TestEveryGPUMustFitNotJustTheSum(t *testing.T) {
	gpus := []Device{
		{Index: 0, FreeBytes: 15998 * mib, TotalBytes: 16380 * mib},
		{Index: 1, FreeBytes: 2000 * mib, TotalBytes: 16380 * mib},
	}
	est := Estimate{Weights: 13 * gib, KV: gib, Compute: gib, Overhead: deviceOverhead, Factor: 1}
	res := Check(est, Target{Kind: TargetAuto}, gpus, Device{}, defaultMargins)
	if res.Need() >= res.Free() {
		t.Fatalf("test setup: total need %s should be below total free %s", FormatBytes(res.Need()), FormatBytes(res.Free()))
	}
	if res.Verdict != NoFit || res.Shares[1].Verdict != NoFit || res.Shares[0].Verdict == NoFit {
		t.Fatalf("verdict %v shares %+v", res.Verdict, res.Shares)
	}
}

func TestTightAndCPU(t *testing.T) {
	gpu := []Device{{Index: 0, FreeBytes: 10 * gib, TotalBytes: 12 * gib}}
	est := Estimate{Weights: 9 * gib, Overhead: 384 * mib, Factor: 1}
	if v := Check(est, Target{Kind: TargetAuto}, gpu, Device{}, defaultMargins).Verdict; v != Tight {
		t.Fatalf("verdict %v, want tight", v)
	}
	ram := Device{FreeBytes: 100 * gib, TotalBytes: 120 * gib}
	cpu := Check(est, Target{Kind: TargetCPU}, gpu, ram, defaultMargins)
	if cpu.Verdict != Fits || len(cpu.Shares) != 1 || cpu.Shares[0].Margin != 6*gib {
		t.Fatalf("cpu %+v", cpu)
	}
	if v := Check(est, Target{Kind: TargetCPU}, gpu, Device{}, defaultMargins).Verdict; v != Unknown {
		t.Fatalf("cpu without meminfo: %v", v)
	}
	if v := Check(est, Target{Kind: TargetAuto}, nil, ram, defaultMargins).Verdict; v != Unknown {
		t.Fatalf("auto without GPUs: %v", v)
	}
	if v := Check(est, Target{Kind: TargetGPU, GPU: 3}, gpu, ram, defaultMargins).Verdict; v != Unknown {
		t.Fatalf("missing GPU: %v", v)
	}
}

func TestRuntimeFlags(t *testing.T) {
	if f := (Target{Kind: TargetAuto}).RuntimeFlags(); len(f) != 0 {
		t.Fatalf("auto flags %v", f)
	}
	if f := (Target{Kind: TargetGPU, GPU: 1}).RuntimeFlags(); len(f) != 2 || f[0] != "--device" || f[1] != "CUDA1" {
		t.Fatalf("gpu flags %v", f)
	}
	if f := (Target{Kind: TargetCPU}).RuntimeFlags(); len(f) != 2 || f[1] != "none" {
		t.Fatalf("cpu flags %v", f)
	}
}

func TestLearnedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit", "usage.json")
	l := LoadLearned(path)
	tgt := Target{Kind: TargetAuto}
	if ok, err := l.Record("ai/m:1", 8192, tgt, map[int]int64{0: 100 * mib}); ok || err != nil {
		t.Fatalf("tiny delta recorded: %v %v", ok, err)
	}
	if ok, err := l.Record("ai/m:1", 8192, tgt, map[int]int64{0: 7 * gib, 1: 7 * gib}); !ok || err != nil {
		t.Fatalf("record: %v %v", ok, err)
	}
	if ok, _ := l.Record("ai/m:1", 8192, Target{Kind: TargetCPU}, map[int]int64{0: 7 * gib}); ok {
		t.Fatal("CPU usage should not be learned from GPU deltas")
	}
	again := LoadLearned(path)
	u, ok := again.Get("ai/m:1", 8192, tgt)
	if !ok || u.Total() != 14*gib {
		t.Fatalf("reloaded %+v %v", u, ok)
	}
	est := EstimateFromUsage(u, 8192)
	if !est.Measured || est.Total(2) != 14*gib {
		t.Fatalf("estimate total %s", FormatBytes(est.Total(2)))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestFormat(t *testing.T) {
	if FormatTokens(32768) != "32k" || FormatTokens(4000) != "4000" {
		t.Fatal("FormatTokens")
	}
	if FormatBytes(3*gib/2) != "1.5 GiB" || FormatBytes(512*mib) != "512 MiB" {
		t.Fatal("FormatBytes")
	}
}

func kv(k string, v any) gguf.KV { return gguf.KV{Key: k, Value: v} }
