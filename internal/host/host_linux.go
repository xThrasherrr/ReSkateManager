package host

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// clockTick is USER_HZ, which is 100 on every Linux build that matters.
const clockTick = 10 * time.Millisecond

// /proc/stat and /proc/meminfo are the host's even inside a Docker container
// (without LXCFS), so the page shows the whole machine there too.
func read() (Reading, error) {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return Reading{}, err
	}
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	if r.Busy, r.Total, err = parseStat(stat); err != nil {
		return Reading{}, err
	}
	if r.MemTotal, r.MemAvailable, r.MemFree, err = parseMeminfo(meminfo); err != nil {
		return Reading{}, err
	}
	if self, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		r.LimitMax, r.LimitUsed = cgroupLimit(os.DirFS("/sys/fs/cgroup"), string(self))
		if r.LimitMax >= r.MemTotal {
			r.LimitMax, r.LimitUsed = 0, 0
		}
	}
	return r, nil
}

// parseStat reads the machine's CPU time from the "cpu" line of /proc/stat:
// user nice system idle iowait irq softirq steal guest guest_nice, in clock
// ticks. Guest time is already part of user and nice, so it is left out.
func parseStat(b []byte) (busy, total time.Duration, err error) {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, errors.New("unreadable /proc/stat")
	}
	var t [8]uint64
	for i := range t {
		if i+1 < len(f) {
			t[i], _ = strconv.ParseUint(f[i+1], 10, 64)
		}
	}
	var sum uint64
	for _, v := range t {
		sum += v
	}
	idle := t[3] + t[4] // idle and iowait
	return time.Duration(sum-idle) * clockTick, time.Duration(sum) * clockTick, nil
}

// parseMeminfo reads /proc/meminfo, whose values are in kB.
func parseMeminfo(b []byte) (total, available, free uint64, err error) {
	vals := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64); err == nil {
			vals[k] = n * 1024
		}
	}
	total, free = vals["MemTotal"], vals["MemFree"]
	if total == 0 {
		return 0, 0, 0, errors.New("unreadable /proc/meminfo")
	}
	available, ok := vals["MemAvailable"]
	if !ok { // kernels before 3.14
		available = free + vals["Buffers"] + vals["Cached"]
	}
	return total, min(available, total), min(free, total), nil
}

// cgroupLimit finds the memory limit of the cgroup /proc/self/cgroup names,
// in the cgroup filesystem cg (/sys/fs/cgroup). Inside a container that
// filesystem's root is often the container's own cgroup while the path still
// names it as the host sees it, so the root is tried too.
func cgroupLimit(cg fs.FS, self string) (limit, used uint64) {
	for _, line := range strings.Split(self, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		var dirs []string
		var limitFile, usageFile, inactive string
		switch {
		case parts[0] == "0" && parts[1] == "": // cgroup v2
			dirs = []string{rel(parts[2]), "."}
			limitFile, usageFile, inactive = "memory.max", "memory.current", "inactive_file"
		case slices.Contains(strings.Split(parts[1], ","), "memory"): // cgroup v1
			dirs = []string{path.Join("memory", rel(parts[2])), "memory"}
			limitFile, usageFile, inactive = "memory.limit_in_bytes", "memory.usage_in_bytes", "total_inactive_file"
		default:
			continue
		}
		for _, dir := range dirs {
			b, err := fs.ReadFile(cg, path.Join(dir, limitFile))
			if err != nil {
				continue
			}
			limit, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
			if err != nil {
				return 0, 0 // "max": no limit
			}
			b, _ = fs.ReadFile(cg, path.Join(dir, usageFile))
			used, _ = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
			b, _ = fs.ReadFile(cg, path.Join(dir, "memory.stat"))
			for _, l := range strings.Split(string(b), "\n") {
				if k, v, ok := strings.Cut(l, " "); ok && k == inactive {
					n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
					used -= min(n, used)
				}
			}
			return limit, used
		}
	}
	return 0, 0
}

// rel makes a cgroup path one an fs.FS takes: no leading slash, "." for the root.
func rel(p string) string {
	if p = strings.Trim(path.Clean("/"+p), "/"); p == "" {
		return "."
	}
	return p
}
