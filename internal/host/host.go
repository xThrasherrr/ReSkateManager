// Package host reads what the machine the manager runs on is doing, CPU and
// memory, for the panel's host performance page.
package host

import "time"

// Reading is the machine at one moment. CPU load is the change in Busy over
// the change in Total between two readings.
type Reading struct {
	Busy  time.Duration // CPU time every core together spent working
	Total time.Duration // CPU time every core together had, idle included

	MemTotal uint64 // physical memory, bytes
	// MemAvailable is what programs could still get without swapping: free
	// memory plus the cache the system hands back when asked.
	MemAvailable uint64
	MemFree      uint64 // holding nothing at all, not even cache

	// LimitMax is the memory limit of the manager's cgroup (a container's
	// limit, or a systemd unit's MemoryMax), 0 without one or with one at or
	// above MemTotal. LimitUsed is what the cgroup holds against it, leaving
	// out cache it can drop, as `docker stats` counts it.
	LimitMax, LimitUsed uint64
}

// Read takes a reading of the machine.
func Read() (Reading, error) { return read() }

// MemUsed is the memory programs hold. Cache the system can hand back counts
// as available, so a Linux box whose free RAM went to the page cache isn't
// shown as full.
func (r Reading) MemUsed() uint64 { return r.MemTotal - min(r.MemAvailable, r.MemTotal) }

// MemCached is the cache MemAvailable counts on getting back. Windows counts
// its standby cache as available without saying how much it is, so there
// this is 0.
func (r Reading) MemCached() uint64 { return r.MemAvailable - min(r.MemFree, r.MemAvailable) }

// CPU is the share of the machine's CPU time spent working between two
// readings, in percent.
func CPU(prev, cur Reading) float64 {
	total := cur.Total - prev.Total
	if total <= 0 {
		return 0
	}
	return min(max(float64(cur.Busy-prev.Busy)/float64(total)*100, 0), 100)
}
