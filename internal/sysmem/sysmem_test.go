package sysmem

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := "MemTotal:       65536000 kB\nMemFree:  1000 kB\nMemAvailable:   60000000 kB\n"
	info, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if info.TotalBytes != 65536000*1024 || info.AvailableBytes != 60000000*1024 {
		t.Fatalf("%+v", info)
	}
	if _, err := Parse(strings.NewReader("garbage\n")); err == nil {
		t.Fatal("missing MemTotal accepted")
	}
}
