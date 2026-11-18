package gguf

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// KV is one metadata entry for Write.
type KV struct {
	Key   string
	Value any
}

// Write encodes a minimal GGUF v3 file: the metadata, one placeholder
// tensor entry per name in tensors, alignment padding, and dataSize zero
// bytes of tensor data. It exists for tests and fixtures; supported values
// are string, bool, uint32, int32, uint64, float32, []int32, []bool and
// []string.
func Write(w io.Writer, kvs []KV, tensors []string, dataSize int64) error {
	e := encoder{w: w}
	e.u32(magic)
	e.u32(3)
	e.u64(uint64(len(tensors)))
	e.u64(uint64(len(kvs)))
	for _, kv := range kvs {
		e.str(kv.Key)
		switch v := kv.Value.(type) {
		case string:
			e.u32(typeString)
			e.str(v)
		case bool:
			e.u32(typeBool)
			e.bool(v)
		case uint32:
			e.u32(typeUint32)
			e.u32(v)
		case int32:
			e.u32(typeInt32)
			e.u32(uint32(v))
		case uint64:
			e.u32(typeUint64)
			e.u64(v)
		case float32:
			e.u32(typeFloat32)
			e.u32(math.Float32bits(v))
		case []int32:
			e.u32(typeArray)
			e.u32(typeInt32)
			e.u64(uint64(len(v)))
			for _, x := range v {
				e.u32(uint32(x))
			}
		case []bool:
			e.u32(typeArray)
			e.u32(typeBool)
			e.u64(uint64(len(v)))
			for _, x := range v {
				e.bool(x)
			}
		case []string:
			e.u32(typeArray)
			e.u32(typeString)
			e.u64(uint64(len(v)))
			for _, x := range v {
				e.str(x)
			}
		default:
			return fmt.Errorf("gguf write: unsupported value %T for %q", kv.Value, kv.Key)
		}
	}
	for _, name := range tensors {
		e.str(name)
		e.u32(1)
		e.u64(1)
		e.u32(0)
		e.u64(0)
	}
	if rem := e.n % defaultAlign; rem != 0 {
		e.write(make([]byte, defaultAlign-rem))
	}
	if dataSize > 0 {
		e.write(make([]byte, dataSize))
	}
	return e.err
}

type encoder struct {
	w   io.Writer
	n   int64
	err error
}

func (e *encoder) write(b []byte) {
	if e.err != nil {
		return
	}
	n, err := e.w.Write(b)
	e.n += int64(n)
	e.err = err
}

func (e *encoder) u32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	e.write(b[:])
}

func (e *encoder) u64(v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	e.write(b[:])
}

func (e *encoder) bool(v bool) {
	if v {
		e.write([]byte{1})
		return
	}
	e.write([]byte{0})
}

func (e *encoder) str(s string) {
	e.u64(uint64(len(s)))
	e.write([]byte(s))
}
