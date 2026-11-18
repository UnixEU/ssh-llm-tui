package gpuinfo

import (
	"strings"
	"testing"
)

func TestParseQuery(t *testing.T) {
	gpus := "0, NVIDIA Example GPU, GPU-abc, 1500, 20475, 47, 35.50\n"
	apps := "GPU-abc, 1400\n"
	snap, err := ParseQuery(gpus, apps)
	if err != nil || !snap.OK || len(snap.GPUs) != 1 {
		t.Fatalf("%+v %v", snap, err)
	}
	g := snap.GPUs[0]
	if g.Index != 0 || g.MemUsedMiB != 1500 || g.MemTotalMiB != 20475 || g.TempC != 47 {
		t.Fatalf("%+v", g)
	}
	if !g.PowerKnown || g.PowerW < 35 || g.PowerW > 36 {
		t.Fatalf("power %+v", g)
	}
	if g.AppsMiB != 1400 {
		t.Fatalf("apps %d", g.AppsMiB)
	}
	if !strings.Contains(g.Name, "Example GPU") {
		t.Fatal(g.Name)
	}
}

func TestParseQueryFractionalAndNA(t *testing.T) {
	// Fractional temperature and power. Atoi would turn 60.06 into 0.
	line := "0, NVIDIA Example GPU, GPU-abc, 18583, 20475, 60.06, 9.62\n"
	snap, err := ParseQuery(line, "GPU-abc, 14000.4\n")
	if err != nil || !snap.OK {
		t.Fatalf("%+v %v", snap, err)
	}
	g := snap.GPUs[0]
	if g.TempC < 60.05 || g.TempC > 60.07 || !g.TempKnown {
		t.Fatalf("temp %+v", g)
	}
	if g.MemUsedMiB != 18583 || g.MemTotalMiB != 20475 {
		t.Fatalf("mem %+v", g)
	}
	if g.PowerW < 9.6 || g.PowerW > 9.7 || !g.PowerKnown {
		t.Fatalf("power %+v", g)
	}
	if g.AppsMiB != 14000 {
		t.Fatalf("apps %d", g.AppsMiB)
	}

	whole, err := ParseQuery("1, Name, GPU-y, 100, 200, 47, 12\n", "")
	if err != nil || whole.GPUs[0].TempC != 47 || !whole.GPUs[0].TempKnown {
		t.Fatalf("integer temp %+v %v", whole.GPUs[0], err)
	}

	fracMem, err := ParseQuery("0, Name, GPU-z, 10.6, 20.2, 30.4, 1.2\n", "")
	if err != nil || fracMem.GPUs[0].MemUsedMiB != 11 || fracMem.GPUs[0].MemTotalMiB != 20 {
		t.Fatalf("rounded mem %+v %v", fracMem.GPUs[0], err)
	}

	for _, raw := range []string{
		"0, Name, GPU-n, [N/A], [N/A], [N/A], [N/A]\n",
		"0, Name, GPU-n, N/A, N/A, N/A, N/A\n",
	} {
		na, err := ParseQuery(raw, "")
		if err != nil || !na.OK {
			t.Fatalf("%q %+v %v", raw, na, err)
		}
		got := na.GPUs[0]
		if got.TempKnown || got.TempC != 0 || got.PowerKnown || got.MemUsedMiB != 0 || got.MemTotalMiB != 0 {
			t.Fatalf("%q parsed as %+v", raw, got)
		}
	}
}

func TestParseQueryPowerNA(t *testing.T) {
	snap, err := ParseQuery("0, Name, GPU-x, 0, 20475, 30, [N/A]\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.GPUs[0].PowerKnown {
		t.Fatal("power should be unknown")
	}
	if snap.GPUs[0].AppsMiB != 0 {
		t.Fatal(snap.GPUs[0].AppsMiB)
	}
}

func TestParseQueryEmpty(t *testing.T) {
	snap, err := ParseQuery("", "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.OK || snap.Message != "GPU n/a" {
		t.Fatalf("%+v", snap)
	}
}
