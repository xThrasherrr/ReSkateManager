// Package perf samples each running server's CPU, memory and player count,
// and the machine as a whole, into the database for the panel's performance
// charts.
package perf

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/host"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
	"github.com/xThrasherrr/ReSkateManager/internal/supervisor"
)

// Interval is how often samples are taken; Keep, how long they are kept.
const (
	Interval = 30 * time.Second
	Keep     = 7 * 24 * time.Hour
)

// Recorder samples every running server, and the machine, every Interval.
type Recorder struct {
	Store *store.Store
	Reg   *instance.Registry
	Log   *slog.Logger
	last  map[string]reading // by instance id
	host  *host.Reading      // the machine at the last sample
	// hostFailed is set once a failed reading of the machine was logged, so
	// one that keeps failing logs once.
	hostFailed bool
}

// reading is a process's CPU time at one moment; CPU load is the change
// between two readings of the same process.
type reading struct {
	pid int
	cpu time.Duration
	at  time.Time
}

// Run takes samples until ctx ends, dropping those older than Keep.
func (r *Recorder) Run(ctx context.Context) {
	r.prune(ctx)
	// A first reading of the machine, so the first tick has CPU load to record.
	if h, err := host.Read(); err == nil {
		r.host = &h
	}
	tick := time.NewTicker(Interval)
	defer tick.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.sample(ctx)
		case <-prune.C:
			r.prune(ctx)
		}
	}
}

func (r *Recorder) prune(ctx context.Context) {
	before := time.Now().Add(-Keep).Unix()
	if err := r.Store.PrunePerf(ctx, before); err != nil && ctx.Err() == nil {
		r.Log.Warn("prune performance samples", "err", err)
	}
	if err := r.Store.PruneHost(ctx, before); err != nil && ctx.Err() == nil {
		r.Log.Warn("prune host samples", "err", err)
	}
	if err := r.Store.PruneNet(ctx, before); err != nil && ctx.Err() == nil {
		r.Log.Warn("prune network samples", "err", err)
	}
}

func (r *Recorder) sample(ctx context.Context) {
	// One time for the whole tick, so the host page can line every server's
	// samples up with the machine's.
	now := time.Now()
	hs := store.HostSample{At: now.Unix()}
	next := map[string]reading{}
	for _, in := range r.Reg.List() {
		hs.Servers++
		u, v, err := in.Usage()
		if err != nil {
			continue
		}
		if v.State == instance.Running {
			hs.Running++
		}
		hs.Players += v.Players
		cur := reading{pid: v.PID, cpu: u.CPU, at: now}
		next[in.ID()] = cur
		prev, ok := r.last[in.ID()]
		if !ok || prev.pid != cur.pid || v.StartedAt == 0 {
			continue
		}
		cpu := float64(cur.cpu-prev.cpu) / float64(cur.at.Sub(prev.at)) / float64(runtime.NumCPU()) * 100
		s := store.PerfSample{At: now.Unix(), Run: v.StartedAt / 1000, CPU: min(max(cpu, 0), 100), Mem: int64(u.Mem), Players: v.Players}
		if err := r.Store.AddPerfSample(ctx, in.ID(), s); err != nil && ctx.Err() == nil {
			r.Log.Warn("store performance sample", "instance", in.ID(), "err", err)
			continue
		}
		hs.ServersCPU += s.CPU
		hs.ServersMem += s.Mem
	}
	r.last = next
	r.sampleHost(ctx, hs)
}

// sampleHost records the machine alongside the servers' sums in hs.
func (r *Recorder) sampleHost(ctx context.Context, hs store.HostSample) {
	h, err := host.Read()
	if err != nil {
		if !r.hostFailed {
			r.Log.Warn("read the machine's CPU and memory", "err", err)
			r.hostFailed = true
		}
		r.host = nil
		return
	}
	r.hostFailed = false
	prev := r.host
	r.host = &h
	if prev == nil {
		return
	}
	hs.CPU = host.CPU(*prev, h)
	hs.MemTotal, hs.MemUsed, hs.MemCached = int64(h.MemTotal), int64(h.MemUsed()), int64(h.MemCached())
	hs.LimitMax, hs.LimitUsed = int64(h.LimitMax), int64(h.LimitUsed)
	if u, err := supervisor.UsageOf(os.Getpid()); err == nil {
		hs.ManagerMem = int64(u.Mem)
	}
	if err := r.Store.AddHostSample(ctx, hs); err != nil && ctx.Err() == nil {
		r.Log.Warn("store host sample", "err", err)
	}
}
