package supervisor

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// clockTick is USER_HZ, which is 100 on every Linux build that matters.
const clockTick = 10 * time.Millisecond

func usage(pid int) (Usage, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return Usage{}, err
	}
	// The command name in field 2 may hold spaces and parentheses; the fields
	// after its last ")" start at field 3 (state).
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return Usage{}, errors.New("unreadable /proc stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 22 {
		return Usage{}, errors.New("unreadable /proc stat")
	}
	utime, _ := strconv.ParseUint(f[11], 10, 64) // field 14
	stime, _ := strconv.ParseUint(f[12], 10, 64) // field 15
	rss, _ := strconv.ParseUint(f[21], 10, 64)   // field 24, in pages
	return Usage{CPU: time.Duration(utime+stime) * clockTick, Mem: rss * uint64(os.Getpagesize())}, nil
}
