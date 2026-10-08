package host

import (
	"testing"
	"testing/fstest"
	"time"
)

func TestParseStat(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	busy, total, err := parseStat([]byte("cpu  100 20 30 800 50 5 5 10 40 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Guest time (40) is part of user already; idle and iowait aren't busy.
	if total != 1020*clockTick || busy != 170*clockTick {
		t.Errorf("busy %v total %v", busy, total)
	}
	// Old kernels stop after iowait.
	if busy, total, err = parseStat([]byte("cpu 10 0 10 70 10\n")); err != nil || busy != 20*clockTick || total != 100*clockTick {
		t.Errorf("short line: busy %v total %v err %v", busy, total, err)
	}
	if _, _, err := parseStat([]byte("intr 1 2 3\n")); err == nil {
		t.Error("read a line that isn't cpu")
	}
}

func TestParseMeminfo(t *testing.T) {
	total, avail, free, err := parseMeminfo([]byte("MemTotal:        8000 kB\nMemFree:          500 kB\nMemAvailable:    6000 kB\nBuffers:          100 kB\nCached:          5000 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if total != 8000*1024 || avail != 6000*1024 || free != 500*1024 {
		t.Errorf("total %d avail %d free %d", total, avail, free)
	}
	r := Reading{MemTotal: total, MemAvailable: avail, MemFree: free}
	if r.MemUsed() != 2000*1024 || r.MemCached() != 5500*1024 {
		t.Errorf("used %d cached %d", r.MemUsed(), r.MemCached())
	}
	// Without MemAvailable, free memory plus buffers and cache.
	if _, avail, _, _ = parseMeminfo([]byte("MemTotal: 8000 kB\nMemFree: 500 kB\nBuffers: 100 kB\nCached: 5000 kB\n")); avail != 5600*1024 {
		t.Errorf("estimated available %d", avail)
	}
	if _, _, _, err := parseMeminfo([]byte("nothing here\n")); err == nil {
		t.Error("read meminfo without MemTotal")
	}
}

func TestCgroupLimit(t *testing.T) {
	v2 := fstest.MapFS{
		"memory.max":     {Data: []byte("2147483648\n")},
		"memory.current": {Data: []byte("1500000000\n")},
		"memory.stat":    {Data: []byte("anon 900000000\nfile 600000000\ninactive_file 500000000\n")},
		"system.slice/reskate-manager.service/memory.max":     {Data: []byte("max\n")},
		"system.slice/reskate-manager.service/memory.current": {Data: []byte("1000\n")},
	}
	for _, c := range []struct {
		name, self  string
		fs          fstest.MapFS
		limit, used uint64
	}{
		{"v2 container, own namespace", "0::/\n", v2, 2147483648, 1000000000},
		{"v2 container, host's path", "0::/docker/abc123\n", v2, 2147483648, 1000000000},
		{"v2 unit without a limit", "0::/system.slice/reskate-manager.service\n", v2, 0, 0},
		{"v1 container", "12:cpu,cpuacct:/docker/abc\n4:memory:/docker/abc\n", fstest.MapFS{
			"memory/memory.limit_in_bytes": {Data: []byte("1073741824\n")},
			"memory/memory.usage_in_bytes": {Data: []byte("800000000\n")},
			"memory/memory.stat":           {Data: []byte("cache 300000000\ntotal_inactive_file 200000000\n")},
		}, 1073741824, 600000000},
		{"no cgroup files", "0::/\n", fstest.MapFS{}, 0, 0},
	} {
		if limit, used := cgroupLimit(c.fs, c.self); limit != c.limit || used != c.used {
			t.Errorf("%s: limit %d used %d, want %d and %d", c.name, limit, used, c.limit, c.used)
		}
	}
}

func TestCPU(t *testing.T) {
	a := Reading{Busy: time.Second, Total: 10 * time.Second}
	b := Reading{Busy: 4 * time.Second, Total: 16 * time.Second}
	if got := CPU(a, b); got != 50 {
		t.Errorf("CPU %v", got)
	}
	if got := CPU(b, b); got != 0 {
		t.Errorf("no time passed: %v", got)
	}
}
