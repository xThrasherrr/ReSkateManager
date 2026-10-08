package alerts

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/host"
)

// The Watcher alerts once when a drive runs low or memory stays high, and
// once more when each is back to normal.
func TestWatcher(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	set := Defaults
	set.Webhook = "https://discord.com/api/webhooks/1/x"
	if err := Save(ctx, s, set); err != nil {
		t.Fatal(err)
	}
	const gb = 1 << 30
	disk := host.Space{Volume: "/data", ID: "1", Total: 80 * gb, Free: 20 * gb}
	mem := host.Reading{MemTotal: 8 * gb, MemAvailable: 4 * gb}
	var sent []Alert
	w := &Watcher{Store: s, Send: func(a Alert) { sent = append(sent, a) }, Dirs: func() []string { return []string{"/data"} },
		Log:    slog.New(slog.DiscardHandler),
		read:   func() (host.Reading, error) { return mem, nil },
		drives: func([]string) ([]host.Space, map[string]string) { return []host.Space{disk}, nil }}
	t0 := time.Now()
	at := func(min float64) time.Time { return t0.Add(time.Duration(min * float64(time.Minute))) }
	expect := func(when string, want ...string) {
		t.Helper()
		var got []string
		for _, a := range sent {
			got = append(got, a.Title)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: sent %q, want %q", when, got, want)
		}
		sent = nil
	}

	w.Check(ctx, at(0))
	expect("all fine")
	disk.Free = 9 * gb
	w.Check(ctx, at(0.5))
	expect("drive low", "Disk space is low on /data")
	disk.Free = 8 * gb
	w.Check(ctx, at(1))
	disk.Free = 10*gb + gb/2 // above the line, but not by the margin
	w.Check(ctx, at(1.5))
	expect("still low")
	disk.Free = 11*gb + gb/2
	w.Check(ctx, at(2))
	expect("room again", "Disk space is back on /data")

	// Memory must stay past the line for MemMinutes, either way.
	mem.MemAvailable = gb / 2 // 94% in use
	w.Check(ctx, at(3))
	w.Check(ctx, at(8))
	mem.MemAvailable = 2 * gb // a dip starts the wait over
	w.Check(ctx, at(9))
	mem.MemAvailable = gb / 2
	w.Check(ctx, at(10))
	w.Check(ctx, at(19.5))
	expect("not high for long enough")
	w.Check(ctx, at(20))
	expect("high for 10 minutes", "Memory is running high")
	mem.MemAvailable = 3 * gb
	w.Check(ctx, at(21))
	w.Check(ctx, at(31))
	expect("back down", "Memory is back down")

	// A container nearer its limit than the machine is to full speaks for it.
	mem.LimitMax, mem.LimitUsed = 2*gb, 2*gb-gb/10
	w.Check(ctx, at(32))
	w.Check(ctx, at(42))
	if len(sent) != 1 || sent[0].Title != "The manager's container is running out of memory" || !strings.Contains(sent[0].Text, "95% in use (1.9 GB of 2.0 GB allowed) for 10 minutes") {
		t.Errorf("container: %+v", sent)
	}
	sent = nil

	// Turned off, the low drive is forgotten; turned on again, it alerts again.
	disk.Free = 5 * gb
	set.Off = []string{DiskLow}
	Save(ctx, s, set)
	w.Check(ctx, at(43))
	expect("disk alerts off")
	set.Off = nil
	Save(ctx, s, set)
	w.Check(ctx, at(44))
	expect("disk alerts on again", "Disk space is low on /data")
}

func TestForDiskAndMemory(t *testing.T) {
	const gb = 1 << 30
	low := forDisk(host.Space{Volume: `C:\`, Total: 500 * gb, Free: 9*gb + gb/2}, 10*gb, "https://p/host", false)
	if low.Kind != DiskLow || low.Clear || !strings.HasPrefix(low.Text, "9.5 GB free of 500.0 GB, under the 10.0 GB set to alert at.") || low.Link != "https://p/host" {
		t.Errorf("low: %+v", low)
	}
	if ok := forDisk(host.Space{Volume: `C:\`, Total: 500 * gb, Free: 12 * gb}, 10*gb, "", true); !ok.Clear || ok.Text != "12.0 GB free of 500.0 GB." {
		t.Errorf("ok: %+v", ok)
	}
	if m := forMemory(memory{pct: 91.2, used: 7 * gb, total: 8 * gb}, 1, "", true); !m.Clear || m.Text != "91% in use (7.0 GB of 8.0 GB) for 1 minute." {
		t.Errorf("memory: %+v", m)
	}
}
