// Package gguf reads the header of a GGUF file: the key/value metadata and
// the tensor table. It never reads tensor data, so a multi-gigabyte model is
// inspected by reading only its first few megabytes.
//
// Format reference: https://github.com/ggml-org/ggml/blob/master/docs/gguf.md
package gguf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

const (
	magic = 0x46554747 // "GGUF" little endian

	maxKeyLen      = 1 << 16
	maxStringLen   = 16 << 20
	maxKVCount     = 1 << 20
	maxTensorCount = 1 << 22
	maxArrayLen    = 1 << 26
	maxDims        = 8
	// keptArrayLen is the longest numeric or bool array kept in Metadata.
	// Per-layer arrays (head counts, sliding window patterns) are short.
	// Long arrays such as tokenizer tables are skipped; their length is
	// still recorded in Header.ArrayLen.
	keptArrayLen = 4096
	defaultAlign = 32
)

// Value types from the GGUF spec.
const (
	typeUint8   = 0
	typeInt8    = 1
	typeUint16  = 2
	typeInt16   = 3
	typeUint32  = 4
	typeInt32   = 5
	typeFloat32 = 6
	typeBool    = 7
	typeString  = 8
	typeArray   = 9
	typeUint64  = 10
	typeInt64   = 11
	typeFloat64 = 12
)

// ErrNotGGUF means the file does not start with the GGUF magic.
var ErrNotGGUF = errors.New("not a GGUF file")

// Header is the parsed header of one GGUF file.
type Header struct {
	Version     uint32
	TensorCount uint64
	// Metadata holds scalar values as int64, uint64, float64, bool or string,
	// and short arrays as []int64, []float64 or []bool. String arrays are
	// not kept.
	Metadata map[string]any
	// ArrayLen is the element count of every array value, kept or not.
	ArrayLen map[string]uint64
	// DataOffset is where tensor data starts.
	DataOffset int64
	// FileSize is the size of the file on disk.
	FileSize int64
}

// DataSize is the byte size of the tensor data section.
func (h Header) DataSize() int64 {
	if h.FileSize <= h.DataOffset {
		return 0
	}
	return h.FileSize - h.DataOffset
}

// ReadFile parses the header of the GGUF file at path.
func ReadFile(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Header{}, err
	}
	h, err := Read(f)
	if err != nil {
		return Header{}, fmt.Errorf("%s: %w", path, err)
	}
	h.FileSize = st.Size()
	return h, nil
}

// Read parses a GGUF header from r. FileSize is left at zero.
func Read(r io.Reader) (Header, error) {
	cr := &countingReader{r: bufio.NewReaderSize(r, 1<<20)}
	d := decoder{r: cr}
	if v := d.u32(); d.err == nil && v != magic {
		return Header{}, ErrNotGGUF
	}
	h := Header{Metadata: map[string]any{}, ArrayLen: map[string]uint64{}}
	h.Version = d.u32()
	if d.err != nil {
		return Header{}, d.err
	}
	if h.Version < 2 || h.Version > 3 {
		return Header{}, fmt.Errorf("unsupported GGUF version %d", h.Version)
	}
	h.TensorCount = d.u64()
	kvCount := d.u64()
	if d.err != nil {
		return Header{}, d.err
	}
	if kvCount > maxKVCount {
		return Header{}, fmt.Errorf("gguf: %d metadata entries exceeds limit", kvCount)
	}
	if h.TensorCount > maxTensorCount {
		return Header{}, fmt.Errorf("gguf: %d tensors exceeds limit", h.TensorCount)
	}
	for i := uint64(0); i < kvCount && d.err == nil; i++ {
		key := d.str(maxKeyLen)
		typ := d.u32()
		if d.err != nil {
			break
		}
		if typ == typeArray {
			elemType := d.u32()
			n := d.u64()
			if d.err != nil {
				break
			}
			if n > maxArrayLen {
				return Header{}, fmt.Errorf("gguf: array %q has %d elements", key, n)
			}
			h.ArrayLen[key] = n
			if v, ok := d.array(elemType, n); ok {
				h.Metadata[key] = v
			}
			continue
		}
		v := d.scalar(typ)
		if d.err == nil {
			h.Metadata[key] = v
		}
	}
	for i := uint64(0); i < h.TensorCount && d.err == nil; i++ {
		d.str(maxKeyLen)
		dims := d.u32()
		if dims > maxDims {
			return Header{}, fmt.Errorf("gguf: tensor with %d dimensions", dims)
		}
		for j := uint32(0); j < dims; j++ {
			d.u64()
		}
		d.u32() // type
		d.u64() // offset
	}
	if d.err != nil {
		return Header{}, fmt.Errorf("gguf: %w", d.err)
	}
	align := int64(defaultAlign)
	if a, ok := Uint(h.Metadata, "general.alignment"); ok && a > 0 && a <= 1<<20 {
		align = int64(a)
	}
	off := cr.n
	if rem := off % align; rem != 0 {
		off += align - rem
	}
	h.DataOffset = off
	return h, nil
}

// String returns a string metadata value.
func String(md map[string]any, key string) (string, bool) {
	s, ok := md[key].(string)
	return s, ok
}

// Uint returns an integer metadata value as uint64. Negative values fail.
func Uint(md map[string]any, key string) (uint64, bool) {
	switch v := md[key].(type) {
	case uint64:
		return v, true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint64(v), true
	case float64:
		if v < 0 || v != math.Trunc(v) {
			return 0, false
		}
		return uint64(v), true
	}
	return 0, false
}

// Ints returns an integer array, or a scalar integer as a one-element slice.
func Ints(md map[string]any, key string) ([]int64, bool) {
	switch v := md[key].(type) {
	case []int64:
		return v, true
	case uint64:
		return []int64{int64(v)}, true
	case int64:
		return []int64{v}, true
	}
	return nil, false
}

// Bools returns a bool array.
func Bools(md map[string]any, key string) ([]bool, bool) {
	v, ok := md[key].([]bool)
	return v, ok
}

type countingReader struct {
	r *bufio.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type decoder struct {
	r   *countingReader
	buf [8]byte
	err error
}

func (d *decoder) read(n int) []byte {
	if d.err != nil {
		return nil
	}
	if _, err := io.ReadFull(d.r, d.buf[:n]); err != nil {
		d.err = unexpected(err)
		return nil
	}
	return d.buf[:n]
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func (d *decoder) u32() uint32 {
	b := d.read(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (d *decoder) u64() uint64 {
	b := d.read(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (d *decoder) skip(n uint64) {
	if d.err != nil {
		return
	}
	if _, err := io.CopyN(io.Discard, d.r, int64(n)); err != nil {
		d.err = unexpected(err)
	}
}

func (d *decoder) str(limit uint64) string {
	n := d.u64()
	if d.err != nil {
		return ""
	}
	if n > limit {
		d.err = fmt.Errorf("string of %d bytes exceeds limit", n)
		return ""
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(d.r, b); err != nil {
		d.err = unexpected(err)
		return ""
	}
	return string(b)
}

func scalarSize(typ uint32) int {
	switch typ {
	case typeUint8, typeInt8, typeBool:
		return 1
	case typeUint16, typeInt16:
		return 2
	case typeUint32, typeInt32, typeFloat32:
		return 4
	case typeUint64, typeInt64, typeFloat64:
		return 8
	}
	return 0
}

func (d *decoder) scalar(typ uint32) any {
	if typ == typeString {
		return d.str(maxStringLen)
	}
	size := scalarSize(typ)
	if size == 0 {
		if d.err == nil {
			d.err = fmt.Errorf("unknown value type %d", typ)
		}
		return nil
	}
	b := d.read(size)
	if b == nil {
		return nil
	}
	switch typ {
	case typeUint8:
		return uint64(b[0])
	case typeInt8:
		return int64(int8(b[0]))
	case typeBool:
		return b[0] != 0
	case typeUint16:
		return uint64(binary.LittleEndian.Uint16(b))
	case typeInt16:
		return int64(int16(binary.LittleEndian.Uint16(b)))
	case typeUint32:
		return uint64(binary.LittleEndian.Uint32(b))
	case typeInt32:
		return int64(int32(binary.LittleEndian.Uint32(b)))
	case typeFloat32:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
	case typeUint64:
		return binary.LittleEndian.Uint64(b)
	case typeInt64:
		return int64(binary.LittleEndian.Uint64(b))
	case typeFloat64:
		return math.Float64frombits(binary.LittleEndian.Uint64(b))
	}
	return nil
}

// array reads n elements. Short numeric and bool arrays are returned; the
// rest are consumed and dropped.
func (d *decoder) array(elemType uint32, n uint64) (any, bool) {
	switch elemType {
	case typeString:
		for i := uint64(0); i < n && d.err == nil; i++ {
			l := d.u64()
			if l > maxStringLen {
				d.err = fmt.Errorf("string of %d bytes exceeds limit", l)
				return nil, false
			}
			d.skip(l)
		}
		return nil, false
	case typeArray:
		d.err = errors.New("nested arrays are not supported")
		return nil, false
	}
	size := scalarSize(elemType)
	if size == 0 {
		d.err = fmt.Errorf("unknown array element type %d", elemType)
		return nil, false
	}
	if n > keptArrayLen {
		d.skip(n * uint64(size))
		return nil, false
	}
	switch elemType {
	case typeBool:
		out := make([]bool, 0, n)
		for i := uint64(0); i < n && d.err == nil; i++ {
			v, _ := d.scalar(elemType).(bool)
			out = append(out, v)
		}
		return out, d.err == nil
	case typeFloat32, typeFloat64:
		out := make([]float64, 0, n)
		for i := uint64(0); i < n && d.err == nil; i++ {
			v, _ := d.scalar(elemType).(float64)
			out = append(out, v)
		}
		return out, d.err == nil
	default:
		out := make([]int64, 0, n)
		for i := uint64(0); i < n && d.err == nil; i++ {
			switch v := d.scalar(elemType).(type) {
			case uint64:
				out = append(out, int64(v))
			case int64:
				out = append(out, v)
			}
		}
		return out, d.err == nil
	}
}
