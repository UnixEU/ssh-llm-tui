// Package sysstat samples host CPU usage and RAM for the panel next to the
// GPU stats. A container without lxcfs reads the host's /proc/stat and
// /proc/meminfo, so the figures are for the whole machine.
package sysstat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/sysmem"
)

// Snapshot is the latest sample.
type Snapshot struct {
	// CPUKnown is false until two samples exist.
	CPUKnown   bool
	CPUPercent float64
	MemKnown   bool
	MemUsed    int64
	MemTotal   int64
}

// Times is the aggregate "cpu" line of /proc/stat.
type Times struct {
	Idle  uint64
	Total uint64
}

// ParseStat reads the first "cpu" line. Idle counts idle and iowait.
func ParseStat(r io.Reader) (Times, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var t Times
		for i, v := range f[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return Times{}, fmt.Errorf("stat: %w", err)
			}
			// guest and guest_nice (9, 10) are already in user and nice.
			if i >= 8 {
				break
			}
			t.Total += n
			if i == 3 || i == 4 {
				t.Idle += n
			}
		}
		return t, nil
	}
	if err := sc.Err(); err != nil {
		return Times{}, err
	}
	return Times{}, fmt.Errorf("stat: no cpu line")
}

// Percent is the busy share between two readings, 0-100.
func Percent(prev, cur Times) (float64, bool) {
	if cur.Total <= prev.Total || cur.Idle < prev.Idle {
		return 0, false
	}
	total := float64(cur.Total - prev.Total)
	idle := float64(cur.Idle - prev.Idle)
	p := (total - idle) / total * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p, true
}

// Poller samples on an interval. One poller serves every session.
type Poller struct {
	interval time.Duration
	statPath string
	readMem  func() (sysmem.Info, error)

	mu   sync.RWMutex
	prev Times
	have bool
	snap Snapshot
}

// NewPoller samples /proc every interval.
func NewPoller(interval time.Duration) *Poller {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Poller{interval: interval, statPath: "/proc/stat", readMem: sysmem.Read}
}

// Start samples until ctx is cancelled. The first sample runs immediately.
func (p *Poller) Start(ctx context.Context) {
	if p == nil {
		return
	}
	p.sample()
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sample()
		}
	}
}

func (p *Poller) sample() {
	var snap Snapshot
	if info, err := p.readMem(); err == nil && info.TotalBytes > 0 {
		snap.MemKnown = true
		snap.MemTotal = info.TotalBytes
		snap.MemUsed = info.TotalBytes - info.AvailableBytes
		if snap.MemUsed < 0 {
			snap.MemUsed = 0
		}
	}
	cur, err := p.readStat()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		if p.have {
			snap.CPUPercent, snap.CPUKnown = Percent(p.prev, cur)
			if !snap.CPUKnown && p.snap.CPUKnown {
				snap.CPUPercent, snap.CPUKnown = p.snap.CPUPercent, true
			}
		}
		p.prev, p.have = cur, true
	}
	p.snap = snap
}

func (p *Poller) readStat() (Times, error) {
	f, err := os.Open(p.statPath)
	if err != nil {
		return Times{}, err
	}
	defer f.Close()
	return ParseStat(f)
}

// SetSnapshot replaces the latest sample. Tests use it.
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
		return Snapshot{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}
