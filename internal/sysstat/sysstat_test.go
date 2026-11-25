package sysstat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/sysmem"
)

const stat1 = "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\n"
const stat2 = "cpu  200 0 200 1300 100 0 0 0 0 0\n"

func TestParseAndPercent(t *testing.T) {
	a, err := ParseStat(strings.NewReader(stat1))
	if err != nil || a.Total != 1000 || a.Idle != 800 {
		t.Fatalf("%+v %v", a, err)
	}
	b, _ := ParseStat(strings.NewReader(stat2))
	// 800 more jiffies, 600 of them idle: 25% busy.
	if p, ok := Percent(a, b); !ok || p != 25 {
		t.Fatalf("percent %v %v", p, ok)
	}
	if _, ok := Percent(b, a); ok {
		t.Fatal("backwards counters accepted")
	}
	if _, err := ParseStat(strings.NewReader("intr 1 2\n")); err == nil {
		t.Fatal("missing cpu line accepted")
	}
}

func TestPollerSample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")
	if err := os.WriteFile(path, []byte(stat1), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(0)
	p.statPath = path
	p.readMem = func() (sysmem.Info, error) {
		return sysmem.Info{TotalBytes: 128 << 30, AvailableBytes: 96 << 30}, nil
	}
	p.sample()
	s := p.Snapshot()
	if s.CPUKnown || !s.MemKnown || s.MemUsed != 32<<30 || s.MemTotal != 128<<30 {
		t.Fatalf("first %+v", s)
	}
	if err := os.WriteFile(path, []byte(stat2), 0o600); err != nil {
		t.Fatal(err)
	}
	p.sample()
	if s = p.Snapshot(); !s.CPUKnown || s.CPUPercent != 25 {
		t.Fatalf("second %+v", s)
	}
}
