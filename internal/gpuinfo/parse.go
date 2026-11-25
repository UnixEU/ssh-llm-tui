// Package gpuinfo reads NVIDIA GPU stats with nvidia-smi. One poller serves
// every SSH session. If the binary is missing the UI shows "GPU n/a".
package gpuinfo

import (
	"bufio"
	"context"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GPU is one device from nvidia-smi.
type GPU struct {
	Index       int
	Name        string
	UUID        string
	MemUsedMiB  int
	MemTotalMiB int
	// MemFreeMiB is memory.free from the driver. When the column is
	// missing it is total minus used.
	MemFreeMiB int
	// TempC is the driver reading in degrees. nvidia-smi on some hosts
	// prints a fraction (60.06); the panel rounds it for display and
	// colours the raw value.
	TempC      float64
	TempKnown  bool
	PowerW     float64
	PowerKnown bool
	// UtilPct is utilization.gpu in percent; UtilKnown is false when the
	// driver does not report it.
	UtilPct   float64
	UtilKnown bool
	AppsMiB   int
}

// Snapshot is the latest sample.
type Snapshot struct {
	// Disabled is true when GPU_STATS=off.
	Disabled bool
	// OK is false when nvidia-smi could not be run or its output did not parse.
	OK      bool
	Message string
	GPUs    []GPU
	At      time.Time
}

// ParseQuery reads the two nvidia-smi CSV blobs.
//
//	nvidia-smi --query-gpu=index,name,uuid,memory.used,memory.total,temperature.gpu,power.draw,memory.free,utilization.gpu --format=csv,noheader,nounits
//	nvidia-smi --query-compute-apps=gpu_uuid,used_memory --format=csv,noheader,nounits
func ParseQuery(gpusCSV, appsCSV string) (Snapshot, error) {
	snap := Snapshot{OK: true, At: time.Now()}
	sc := bufio.NewScanner(strings.NewReader(gpusCSV))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		cols := splitCSV(line)
		if len(cols) < 7 {
			continue
		}
		g := GPU{
			Index:       atoi(cols[0]),
			Name:        cols[1],
			UUID:        cols[2],
			MemUsedMiB:  atoiRounded(cols[3]),
			MemTotalMiB: atoiRounded(cols[4]),
		}
		if t, ok := atof(cols[5]); ok {
			g.TempC = t
			g.TempKnown = true
		}
		if w, ok := atof(cols[6]); ok {
			g.PowerW = w
			g.PowerKnown = true
		}
		g.MemFreeMiB = g.MemTotalMiB - g.MemUsedMiB
		if len(cols) >= 8 && !isNA(cols[7]) && strings.TrimSpace(cols[7]) != "" {
			g.MemFreeMiB = atoiRounded(cols[7])
		}
		if g.MemFreeMiB < 0 {
			g.MemFreeMiB = 0
		}
		if len(cols) >= 9 {
			if u, ok := atof(cols[8]); ok {
				g.UtilPct = u
				g.UtilKnown = true
			}
		}
		snap.GPUs = append(snap.GPUs, g)
	}
	apps := map[string]int{}
	sc = bufio.NewScanner(strings.NewReader(appsCSV))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		cols := splitCSV(line)
		if len(cols) < 2 {
			continue
		}
		apps[cols[0]] += atoiRounded(cols[1])
	}
	for i := range snap.GPUs {
		snap.GPUs[i].AppsMiB = apps[snap.GPUs[i].UUID]
	}
	if len(snap.GPUs) == 0 {
		snap.OK = false
		snap.Message = "GPU n/a"
	}
	return snap, nil
}

func splitCSV(line string) []string {
	parts := strings.Split(line, ",")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

func atoi(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || isNA(s) {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}

// atoiRounded accepts integer and fractional nvidia-smi fields. Atoi turns
// a fractional field such as "60.06" into 0.
func atoiRounded(s string) int {
	n, ok := atof(s)
	if !ok {
		return 0
	}
	return int(math.Round(n))
}

func isNA(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "[]")
	return strings.EqualFold(s, "N/A") || strings.EqualFold(s, "NA")
}

func atof(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || isNA(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Poller runs nvidia-smi on an interval.
type Poller struct {
	mode     string
	interval time.Duration
	bin      string
	mu       sync.RWMutex
	snap     Snapshot
}

// NewPoller builds a poller. mode is "auto" or "off".
func NewPoller(mode, bin string, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if strings.TrimSpace(bin) == "" {
		bin = "nvidia-smi"
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	p := &Poller{mode: mode, interval: interval, bin: bin}
	if mode == "off" {
		p.snap = Snapshot{Disabled: true, Message: "GPU stats off"}
	}
	return p
}

// Start samples until ctx is cancelled. The first sample runs immediately.
func (p *Poller) Start(ctx context.Context) {
	if p == nil || p.mode == "off" {
		return
	}
	p.sample(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sample(ctx)
		}
	}
}

func (p *Poller) sample(ctx context.Context) {
	snap := p.query(ctx)
	p.mu.Lock()
	p.snap = snap
	p.mu.Unlock()
}

func (p *Poller) query(ctx context.Context) Snapshot {
	gpus, err := p.run(ctx, "--query-gpu=index,name,uuid,memory.used,memory.total,temperature.gpu,power.draw,memory.free,utilization.gpu", "--format=csv,noheader,nounits")
	if err != nil {
		return Snapshot{OK: false, Message: "GPU n/a", At: time.Now()}
	}
	apps, err := p.run(ctx, "--query-compute-apps=gpu_uuid,used_memory", "--format=csv,noheader,nounits")
	if err != nil {
		apps = ""
	}
	snap, err := ParseQuery(gpus, apps)
	if err != nil || !snap.OK {
		return Snapshot{OK: false, Message: "GPU n/a", At: time.Now()}
	}
	return snap
}

func (p *Poller) run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Refresh samples now and returns the result, so a fit check does not act
// on a reading that is a poll interval old.
func (p *Poller) Refresh(ctx context.Context) Snapshot {
	if p == nil || p.mode == "off" {
		return p.Snapshot()
	}
	p.sample(ctx)
	return p.Snapshot()
}

// SetSnapshot replaces the latest sample. Tests use it to draw a panel
// without running nvidia-smi.
func (p *Poller) SetSnapshot(s Snapshot) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.snap = s
	p.mu.Unlock()
}

// Snapshot returns the latest sample.
func (p *Poller) Snapshot() Snapshot {
	if p == nil {
		return Snapshot{Disabled: true}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}
