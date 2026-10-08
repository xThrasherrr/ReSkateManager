//go:build windows || linux

package host

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRead(t *testing.T) {
	a, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	// The counters move in clock ticks (10-16 ms), so read until they do.
	b := a
	for deadline := time.Now().Add(2 * time.Second); b.Total == a.Total && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
		if b, err = Read(); err != nil {
			t.Fatal(err)
		}
	}
	if b.Total <= a.Total || b.Busy < a.Busy || b.Busy > b.Total {
		t.Errorf("CPU time went from %+v to %+v", a, b)
	}
	if b.MemTotal < 64<<20 || b.MemAvailable == 0 || b.MemAvailable > b.MemTotal || b.MemFree > b.MemAvailable {
		t.Errorf("memory %+v", b)
	}
	if b.LimitMax != 0 && b.LimitMax >= b.MemTotal {
		t.Errorf("limit %d at or above the machine's %d", b.LimitMax, b.MemTotal)
	}
}

func TestDiskSpace(t *testing.T) {
	dir := t.TempDir()
	s, err := DiskSpace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Volume == "" || s.Total == 0 || s.Used > s.Total || s.Free > s.Total || s.Used+s.Free > s.Total {
		t.Errorf("space %+v", s)
	}
	// A folder not made yet is on its parent's drive.
	if m, err := DiskSpace(filepath.Join(dir, "missing", "too")); err != nil || m.ID != s.ID || m.Volume != s.Volume {
		t.Errorf("missing folder: %+v, %v", m, err)
	}
	drives, vol := Drives([]string{dir, "", filepath.Join(dir, "missing"), dir})
	if len(drives) != 1 || drives[0].ID != s.ID || len(vol) != 2 || vol[dir] != s.Volume || vol[filepath.Join(dir, "missing")] != s.Volume {
		t.Errorf("drives %+v, volumes %v", drives, vol)
	}
}
