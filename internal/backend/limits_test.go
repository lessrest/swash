package backend

import "testing"

func TestParseLimits(t *testing.T) {
	base := Limits{CPUs: 14, CPUWeight: 50, MemoryHigh: 8 << 30, MemoryMax: 12 << 30, SwapMax: 4 << 30, TasksMax: 4096}

	got, err := ParseLimits("cpus=2.5,mem=1G", base)
	if err != nil {
		t.Fatal(err)
	}
	want := Limits{CPUs: 2.5, CPUWeight: 50, MemoryHigh: (1 << 30) / 10 * 9, MemoryMax: 1 << 30, NoSwap: true, TasksMax: 4096}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	got, err = ParseLimits("mem=1G,swap=512M,high=800M,tasks=off", base)
	if err != nil {
		t.Fatal(err)
	}
	if got.SwapMax != 512<<20 || got.NoSwap || got.MemoryHigh != 800<<20 || got.TasksMax != 0 {
		t.Fatalf("got %+v", got)
	}

	if got, _ := ParseLimits("off", base); !got.IsZero() {
		t.Fatalf("off: got %+v", got)
	}
	if _, err := ParseLimits("bogus=1", base); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"512": 512, "4K": 4096, "1.5G": 3 << 29, "2t": 2 << 40} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}
