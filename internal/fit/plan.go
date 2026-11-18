package fit

import (
	"fmt"
	"strconv"
	"strings"
)

// TargetKind is where a model runs.
type TargetKind int

const (
	// TargetAuto lets llama.cpp spread layers over every visible GPU.
	TargetAuto TargetKind = iota
	// TargetGPU confines the model to one GPU.
	TargetGPU
	// TargetCPU keeps the model off the GPUs entirely.
	TargetCPU
)

// Target is a placement choice.
type Target struct {
	Kind TargetKind
	// GPU is the device index for TargetGPU.
	GPU int
}

// Key is a stable identifier used for learned usage.
func (t Target) Key() string {
	switch t.Kind {
	case TargetGPU:
		return "gpu" + strconv.Itoa(t.GPU)
	case TargetCPU:
		return "cpu"
	}
	return "auto"
}

// Label is the UI name.
func (t Target) Label() string {
	switch t.Kind {
	case TargetGPU:
		return fmt.Sprintf("GPU %d", t.GPU)
	case TargetCPU:
		return "CPU (system RAM)"
	}
	return "Auto (all GPUs)"
}

// RuntimeFlags are the llama.cpp flags sent to Docker Model Runner. All of
// them are on DMR's runtime flag allowlist. DMR always starts llama.cpp
// with -ngl 999 and appends these after it.
func (t Target) RuntimeFlags() []string {
	switch t.Kind {
	case TargetGPU:
		return []string{"--device", "CUDA" + strconv.Itoa(t.GPU)}
	case TargetCPU:
		return []string{"--device", "none"}
	}
	return nil
}

// Device is free memory on one GPU, or system RAM for the CPU target.
type Device struct {
	Index      int
	Name       string
	FreeBytes  int64
	TotalBytes int64
}

// Margins is the headroom kept free on each device:
// max(MinBytes, Percent of the device's total memory).
type Margins struct {
	MinBytes int64
	Percent  float64
}

// For returns the margin for a device of the given size.
func (m Margins) For(total int64) int64 {
	pct := int64(float64(total) * m.Percent / 100)
	if pct > m.MinBytes {
		return pct
	}
	return m.MinBytes
}

// Verdict is the outcome of a fit check.
type Verdict int

const (
	// Unknown means there is not enough information to decide.
	Unknown Verdict = iota
	// Fits leaves at least the margin free on every device.
	Fits
	// Tight fits, but eats into the margin on at least one device.
	Tight
	// NoFit needs more memory than is free on at least one device.
	NoFit
)

func (v Verdict) String() string {
	switch v {
	case Fits:
		return "fits"
	case Tight:
		return "tight"
	case NoFit:
		return "does not fit"
	}
	return "unknown"
}

// DeviceShare is one device's part of a placement.
type DeviceShare struct {
	Device  Device
	Need    int64
	Margin  int64
	Verdict Verdict
}

// Result is a fit check for one estimate on one target.
type Result struct {
	Target   Target
	Estimate Estimate
	Shares   []DeviceShare
	Verdict  Verdict
	// Reason explains Unknown.
	Reason string
}

// Need is the total over all devices.
func (r Result) Need() int64 {
	var n int64
	for _, s := range r.Shares {
		n += s.Need
	}
	return n
}

// Free is the total free memory over all devices.
func (r Result) Free() int64 {
	var n int64
	for _, s := range r.Shares {
		n += s.Device.FreeBytes
	}
	return n
}

// Check places est on the devices for target. gpus are the visible GPUs;
// ram is system memory for the CPU target.
//
// Auto uses llama.cpp's default layer split: each GPU takes a share of the
// weights and KV cache proportional to its free memory, and every GPU in
// use holds its own compute buffer and runtime overhead. The projector stays
// on the first GPU. A placement fits only when every device fits; enough
// memory in total is not enough.
func Check(est Estimate, target Target, gpus []Device, ram Device, margins Margins) Result {
	res := Result{Target: target, Estimate: est}
	switch target.Kind {
	case TargetCPU:
		if ram.TotalBytes <= 0 {
			res.Reason = "system memory is unknown"
			return res
		}
		need := est.Model()
		res.Shares = []DeviceShare{share(ram, need, margins)}
	case TargetGPU:
		var dev *Device
		for i := range gpus {
			if gpus[i].Index == target.GPU {
				dev = &gpus[i]
			}
		}
		if dev == nil {
			res.Reason = fmt.Sprintf("GPU %d is not visible", target.GPU)
			return res
		}
		res.Shares = []DeviceShare{share(*dev, est.Model()+est.Overhead, margins)}
	default:
		if len(gpus) == 0 {
			res.Reason = "no GPU statistics (nvidia-smi unavailable)"
			return res
		}
		var free int64
		for _, g := range gpus {
			if g.FreeBytes > 0 {
				free += g.FreeBytes
			}
		}
		f := est.Factor
		if f <= 0 {
			f = 1
		}
		split := int64(float64(est.Weights+est.KV) * f)
		perDevice := int64(float64(est.Compute)*f) + est.Overhead
		proj := int64(float64(est.Projector) * f)
		for i, g := range gpus {
			var part int64
			if free > 0 && g.FreeBytes > 0 {
				part = int64(float64(split) * float64(g.FreeBytes) / float64(free))
			}
			need := part + perDevice
			if i == 0 {
				need += proj
			}
			res.Shares = append(res.Shares, share(g, need, margins))
		}
	}
	res.Verdict = Fits
	for _, s := range res.Shares {
		if s.Verdict > res.Verdict {
			res.Verdict = s.Verdict
		}
	}
	return res
}

func share(d Device, need int64, margins Margins) DeviceShare {
	s := DeviceShare{Device: d, Need: need, Margin: margins.For(d.TotalBytes)}
	switch {
	case need > d.FreeBytes:
		s.Verdict = NoFit
	case need+s.Margin > d.FreeBytes:
		s.Verdict = Tight
	default:
		s.Verdict = Fits
	}
	return s
}

// LargestFitting returns the largest context in choices whose check is Fits
// (or Tight when allowTight), and false when none is.
func LargestFitting(m ModelInfo, choices []int, cacheType string, target Target, gpus []Device, ram Device, margins Margins, allowTight bool) (int, bool) {
	best, ok := 0, false
	for _, c := range CapContexts(choices, m.ContextLength) {
		v := Check(EstimateFor(m, c, cacheType), target, gpus, ram, margins).Verdict
		if v == Fits || (allowTight && v == Tight) {
			if c > best {
				best, ok = c, true
			}
		}
	}
	return best, ok
}

// CapContexts drops choices above the model's trained context. When every
// choice is above it, the trained context itself is the only option. A
// zero max keeps all choices.
func CapContexts(choices []int, max int) []int {
	out := make([]int, 0, len(choices))
	for _, c := range choices {
		if c <= 0 {
			continue
		}
		if max > 0 && c > max {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 && max > 0 {
		out = append(out, max)
	}
	return out
}

// ParseContexts reads "8192,16k,32K" into token counts.
func ParseContexts(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		mult := 1
		if strings.HasSuffix(part, "k") {
			mult = 1024
			part = strings.TrimSuffix(part, "k")
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid context size %q", part)
		}
		out = append(out, n*mult)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no context sizes")
	}
	return out, nil
}
