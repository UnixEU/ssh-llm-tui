// Package sysmem reads host memory figures for the CPU fit check.
// A container without a memory limit sees the host's /proc/meminfo.
package sysmem

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Info is system RAM in bytes and the usable CPU count.
type Info struct {
	TotalBytes     int64
	AvailableBytes int64
	CPUs           int
}

// Read parses /proc/meminfo.
func Read() (Info, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	info, err := Parse(f)
	if err != nil {
		return Info{}, err
	}
	info.CPUs = runtime.NumCPU()
	return info, nil
}

// Parse reads MemTotal and MemAvailable (kB) from meminfo text.
func Parse(r io.Reader) (Info, error) {
	var info Info
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			info.TotalBytes = n * 1024
		case "MemAvailable":
			info.AvailableBytes = n * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return Info{}, err
	}
	if info.TotalBytes == 0 {
		return Info{}, fmt.Errorf("meminfo: MemTotal missing")
	}
	return info, nil
}
