package gguf

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	kvs := []KV{
		{"general.architecture", "llama"},
		{"general.alignment", uint32(32)},
		{"llama.block_count", uint32(32)},
		{"llama.context_length", uint32(131072)},
		{"llama.attention.head_count_kv", uint32(8)},
		{"llama.rope.freq_base", float32(500000)},
		{"test.per_layer", []int32{8, 0, 8}},
		{"test.pattern", []bool{true, false}},
		{"tokenizer.ggml.tokens", []string{"a", "b", "c"}},
	}
	if err := Write(&buf, kvs, []string{"t0", "t1"}, 1000); err != nil {
		t.Fatal(err)
	}
	total := int64(buf.Len())
	h, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	h.FileSize = total
	if h.TensorCount != 2 {
		t.Fatalf("tensor count %d", h.TensorCount)
	}
	if h.DataSize() != 1000 {
		t.Fatalf("data size %d, want 1000 (offset %d of %d)", h.DataSize(), h.DataOffset, total)
	}
	if s, _ := String(h.Metadata, "general.architecture"); s != "llama" {
		t.Fatalf("arch %q", s)
	}
	if v, _ := Uint(h.Metadata, "llama.context_length"); v != 131072 {
		t.Fatalf("ctx %d", v)
	}
	if v, ok := Ints(h.Metadata, "test.per_layer"); !ok || len(v) != 3 || v[1] != 0 {
		t.Fatalf("per layer %v", v)
	}
	if v, ok := Bools(h.Metadata, "test.pattern"); !ok || len(v) != 2 || !v[0] {
		t.Fatalf("pattern %v", v)
	}
	if _, kept := h.Metadata["tokenizer.ggml.tokens"]; kept {
		t.Fatal("string arrays should not be kept")
	}
	if h.ArrayLen["tokenizer.ggml.tokens"] != 3 {
		t.Fatalf("token count %d", h.ArrayLen["tokenizer.ggml.tokens"])
	}
}

func TestReadFileSetsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.gguf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(f, []KV{{"general.architecture", "qwen3"}}, nil, 4096); err != nil {
		t.Fatal(err)
	}
	f.Close()
	h, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.DataSize() != 4096 {
		t.Fatalf("data size %d", h.DataSize())
	}
}

func TestRejectsNonGGUF(t *testing.T) {
	_, err := Read(bytes.NewReader([]byte("PK\x03\x04 not a model")))
	if !errors.Is(err, ErrNotGGUF) {
		t.Fatalf("err = %v", err)
	}
}

func TestTruncatedHeaderFails(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, []KV{{"general.architecture", "llama"}}, []string{"t"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(bytes.NewReader(buf.Bytes()[:20])); err == nil {
		t.Fatal("truncated header parsed")
	}
}

func TestHugeLengthsAreRejected(t *testing.T) {
	var buf bytes.Buffer
	e := encoder{w: &buf}
	e.u32(magic)
	e.u32(3)
	e.u64(0)
	e.u64(1)
	e.u64(1 << 40) // key length
	if _, err := Read(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("huge key length accepted")
	}
}
