package logparse

import (
	"regexp"
	"strconv"
)

// Network is the summary a server logs once a minute while anyone is on, with
// its activity log on:
//
//	[network] 3 players, 40 KB/s out, 12 KB/s in, worst ping 80 ms, 2 KB queued (longest 5 ms),
//	0 failed sends, 1 skipped, 0 dropped | loop 120 passes/s, 4% busy, longest pass 3.1 ms,
//	longest gap 9.0 ms (worst since start: 12.5 ms pass, 30.2 ms gap)
//
// all on one line. What follows the ping is optional, and Full says whether
// it was there.
type Network struct {
	Players int
	OutKBs  int // KB/s sent
	InKBs   int // KB/s received
	PingMs  int // the worst player's

	Full     bool
	QueuedKB int   // waiting to be sent
	QueueMs  int   // how long a message sent now would wait
	Failed   int64 // sends that failed, skipped and dropped: totals since the server started
	Skipped  int64
	Dropped  int64

	Loop    bool    // whether the line had the loop's figures
	BusyPct int     // of the server's main loop, over the last minute
	PassMs  float64 // its longest pass in the last minute
	GapMs   float64 // and the longest gap between passes
}

// The line must match to its end: lines that start with a player's name
// (joins, leaves) always go on after it, so a name can't pass for one.
var networkRe = regexp.MustCompile(`^\[network\] (\d+) players?, (\d+) KB/s out, (\d+) KB/s in, worst ping (\d+) ms` +
	`(?:, (\d+) KB queued \(longest (\d+) ms\), (\d+) failed sends, (\d+) skipped, (\d+) dropped` +
	`(?: \| loop \d+ passes/s, (\d+)% busy, longest pass (\d+(?:\.\d+)?) ms, longest gap (\d+(?:\.\d+)?) ms` +
	` \(worst since start: \d+(?:\.\d+)? ms pass, \d+(?:\.\d+)? ms gap\))?)?$`)

// ParseNetwork reads a server's "[network]" line.
func ParseNetwork(text string) (Network, bool) {
	m := networkRe.FindStringSubmatch(text)
	if m == nil {
		return Network{}, false
	}
	n := Network{Players: atoi(m[1]), OutKBs: atoi(m[2]), InKBs: atoi(m[3]), PingMs: atoi(m[4])}
	if m[5] != "" {
		n.Full = true
		n.QueuedKB, n.QueueMs = atoi(m[5]), atoi(m[6])
		n.Failed, n.Skipped, n.Dropped = atoi64(m[7]), atoi64(m[8]), atoi64(m[9])
	}
	if m[10] != "" {
		n.Loop = true
		n.BusyPct = atoi(m[10])
		n.PassMs, _ = strconv.ParseFloat(m[11], 64)
		n.GapMs, _ = strconv.ParseFloat(m[12], 64)
	}
	return n, true
}

// atoi and atoi64 read digits the pattern matched, which can still be too
// many for an int; those read as the most it holds.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return n
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 1<<63 - 1
	}
	return n
}
