package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/host"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

const (
	// WatchEvery is how often the Watcher looks at the disks and memory.
	WatchEvery = 30 * time.Second
	// diskMargin is how far above the line a low drive's free space must
	// climb to count as fine again, so one hovering at the line alerts once.
	diskMargin = 1 << 30
)

// Watcher alerts when a drive the manager keeps things on runs low on space,
// or memory stays high, and again once each is back to normal: once per
// crossing, not on every look. A kind that is off, or no webhook, forgets
// what it saw, so turning it on reports a problem that is already there.
type Watcher struct {
	Store *store.Store
	Send  func(Alert)
	Dirs  func() []string // the folders whose drives it watches
	Link  func() string   // the Host page's address, or ""; nil for none
	Log   *slog.Logger

	// read and drives are host.Read and host.Drives; tests swap them.
	read   func() (host.Reading, error)
	drives func([]string) ([]host.Space, map[string]string)

	low     map[string]bool // drives alerted as low, by ID
	memHigh bool            // memory alerted as high
	memFrom time.Time       // when memory crossed the line it must now stay past
	readErr bool            // a failed reading of memory was logged
}

// Run looks every WatchEvery until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.Check(ctx, now)
		}
	}
}

// Check looks at the disks and memory once, as of now.
func (w *Watcher) Check(ctx context.Context, now time.Time) {
	set, err := Load(ctx, w.Store)
	if err != nil {
		w.Log.Warn("alert settings", "err", err)
		return
	}
	on := func(kind string) bool { return set.Webhook != "" && !slices.Contains(set.Off, kind) }
	if on(DiskLow) {
		w.checkDisks(set)
	} else {
		w.low = nil
	}
	if on(MemoryHigh) {
		w.checkMemory(set, now)
	} else {
		w.memHigh, w.memFrom = false, time.Time{}
	}
}

func (w *Watcher) checkDisks(set Settings) {
	drives := host.Drives
	if w.drives != nil {
		drives = w.drives
	}
	list, _ := drives(w.Dirs())
	if w.low == nil {
		w.low = map[string]bool{}
	}
	line := uint64(set.DiskGB) << 30
	for _, d := range list {
		switch {
		case !w.low[d.ID] && d.Free < line:
			w.low[d.ID] = true
			w.Send(forDisk(d, line, w.link(), false))
		case w.low[d.ID] && d.Free >= line+diskMargin:
			delete(w.low, d.ID)
			w.Send(forDisk(d, line, w.link(), true))
		}
	}
}

// checkMemory alerts once memory has stayed on the other side of the line
// for MemMinutes, high or back down.
func (w *Watcher) checkMemory(set Settings, now time.Time) {
	read := host.Read
	if w.read != nil {
		read = w.read
	}
	r, err := read()
	if err != nil || r.MemTotal == 0 {
		if !w.readErr {
			w.Log.Warn("read the machine's memory for alerts", "err", err)
			w.readErr = true
		}
		return
	}
	w.readErr = false
	m := memoryUse(r)
	if high := m.pct >= float64(set.MemPct); high == w.memHigh {
		w.memFrom = time.Time{}
		return
	}
	if w.memFrom.IsZero() {
		w.memFrom = now
	}
	if now.Sub(w.memFrom) < time.Duration(set.MemMinutes)*time.Minute {
		return
	}
	w.memHigh, w.memFrom = !w.memHigh, time.Time{}
	w.Send(forMemory(m, set.MemMinutes, w.link(), !w.memHigh))
}

func (w *Watcher) link() string {
	if w.Link == nil {
		return ""
	}
	return w.Link()
}

// memory is how full memory is: the machine's, or the manager's container
// against its limit when it has one and that is fuller.
type memory struct {
	pct         float64
	used, total uint64
	container   bool
}

func memoryUse(r host.Reading) memory {
	m := memory{pct: float64(r.MemUsed()) / float64(r.MemTotal) * 100, used: r.MemUsed(), total: r.MemTotal}
	if r.LimitMax > 0 {
		if pct := float64(r.LimitUsed) / float64(r.LimitMax) * 100; pct > m.pct {
			return memory{pct: pct, used: r.LimitUsed, total: r.LimitMax, container: true}
		}
	}
	return m
}

// forDisk describes drive d running low on space, under line, or having
// room again once ok.
func forDisk(d host.Space, line uint64, link string, ok bool) Alert {
	free := size(d.Free) + " free of " + size(d.Total)
	if ok {
		return Alert{Kind: DiskLow, Title: "Disk space is back on " + d.Volume, Text: free + ".", Link: link, Clear: true}
	}
	return Alert{Kind: DiskLow, Title: "Disk space is low on " + d.Volume, Link: link,
		Text: free + ", under the " + size(line) + " set to alert at. Once it runs out, servers can't write their logs, install mods or update; the Host page shows what is using it."}
}

// forMemory describes memory staying high for minutes, or back down once ok.
func forMemory(m memory, minutes int, link string, ok bool) Alert {
	what, title := "Memory", "Memory is running high"
	if m.container {
		what, title = "The manager's container", "The manager's container is running out of memory"
	}
	use := fmt.Sprintf("%.0f%% in use (%s of %s", m.pct, size(m.used), size(m.total))
	if m.container {
		use += " allowed"
	}
	use += fmt.Sprintf(") for %d %s", minutes, plural(minutes, "minute"))
	if ok {
		return Alert{Kind: MemoryHigh, Title: what + " is back down", Text: use + ".", Link: link, Clear: true}
	}
	return Alert{Kind: MemoryHigh, Title: title, Link: link,
		Text: use + ". When it runs out, servers slow down, crash or are killed; the Host page shows which use the most."}
}

func size(n uint64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
