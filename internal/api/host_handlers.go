package api

import (
	"net/http"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/perf"
)

// hostServer is one server's share of the machine over the range shown.
type hostServer struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	CPU    float64           `json:"cpu"` // average while it ran
	CPUMax float64           `json:"cpuMax"`
	Mem    int64             `json:"mem"` // average while it ran
	MemMax int64             `json:"memMax"`
	Points []hostServerPoint `json:"points"`
}

// hostServerPoint is a server's share of one bucket: its samples summed and
// divided by the machine's, so a server that ran for half the bucket counts
// half, and the servers stack up to the machine's servers total.
type hostServerPoint struct {
	At  int64   `json:"at"`
	CPU float64 `json:"cpu"`
	Mem int64   `json:"mem"`
}

func (a *API) hostPerformance(w http.ResponseWriter, r *http.Request) {
	rg, ok := perfRanges[r.URL.Query().Get("range")]
	if !ok {
		rg = perfRanges["1h"]
	}
	since := time.Now().Add(-rg.span).Unix()
	bucket := int64(rg.bucket / time.Second)
	points, err := a.Store.HostPoints(r.Context(), since, bucket)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	shares, err := a.Store.ServersPerfPoints(r.Context(), since, bucket)
	if err != nil {
		a.internalErr(w, r, err)
		return
	}
	latest, err := a.Store.LatestHost(r.Context())
	if err != nil {
		a.internalErr(w, r, err)
		return
	}

	ticks := map[int64]int{} // host samples per bucket
	for _, p := range points {
		ticks[p.At] = p.Samples
	}
	servers := []*hostServer{}
	byID := map[string]*hostServer{}
	for _, in := range a.Reg.List() {
		s := &hostServer{ID: in.ID(), Name: in.Def().Name, Points: []hostServerPoint{}}
		servers = append(servers, s)
		byID[s.ID] = s
	}
	type sum struct {
		cpu, mem float64
		n        int
	}
	sums := map[string]*sum{}
	for _, p := range shares {
		s := byID[p.Instance]
		if s == nil {
			continue // removed since
		}
		// A tick whose machine reading failed still has the server's sample.
		n := max(p.Samples, ticks[p.At])
		s.Points = append(s.Points, hostServerPoint{At: p.At, CPU: p.CPU / float64(n), Mem: p.Mem / int64(n)})
		s.CPUMax, s.MemMax = max(s.CPUMax, p.CPUMax), max(s.MemMax, p.MemMax)
		if sums[p.Instance] == nil {
			sums[p.Instance] = &sum{}
		}
		t := sums[p.Instance]
		t.cpu, t.mem, t.n = t.cpu+p.CPU, t.mem+float64(p.Mem), t.n+p.Samples
	}
	for id, t := range sums {
		byID[id].CPU, byID[id].Mem = t.cpu/float64(t.n), int64(t.mem/float64(t.n))
	}
	writeJSON(w, 200, map[string]any{"since": since, "bucket": bucket, "interval": int64(perf.Interval / time.Second),
		"points": points, "servers": servers, "latest": latest})
}
