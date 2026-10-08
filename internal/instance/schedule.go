package instance

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Scheduled restarts. A server restarts at set times of day on the manager's
// clock, after so many hours of uptime, or both, whichever comes first.
const (
	// RestartWarning is how long before a scheduled restart players hear of it.
	RestartWarning  = 10 * time.Minute
	MaxRestartTimes = 6
	MaxRestartHours = 7 * 24
)

// ParseRestartTimes checks times of day such as "04:00" and returns them
// sorted, written the same way, without repeats.
func ParseRestartTimes(list []string) ([]string, error) {
	out := []string{}
	for _, s := range list {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("%q is not a time of day like 04:00", s)
		}
		if s = t.Format("15:04"); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > MaxRestartTimes {
		return nil, fmt.Errorf("a server restarts at most %d times a day", MaxRestartTimes)
	}
	slices.Sort(out)
	return out, nil
}

// NextRestart is the server's next scheduled restart after `after`, for a run
// that started at started; zero when it has no schedule. A time of day that
// falls within RestartWarning of the start is skipped, so a server isn't
// restarted just after it came up, or without its players hearing first.
func (d Def) NextRestart(started, after time.Time) time.Time {
	var next time.Time
	if d.RestartHours > 0 {
		step := time.Duration(d.RestartHours) * time.Hour
		next = started.Add(step)
		if !next.After(after) {
			next = next.Add(after.Sub(next).Truncate(step) + step)
		}
	}
	from := after
	if early := started.Add(RestartWarning); early.After(from) {
		from = early
	}
	for _, s := range d.RestartTimes {
		hm, err := time.Parse("15:04", s)
		if err != nil {
			continue
		}
		y, m, day := from.Date()
		t := time.Date(y, m, day, hm.Hour(), hm.Minute(), 0, 0, from.Location())
		if !t.After(from) {
			t = time.Date(y, m, day+1, hm.Hour(), hm.Minute(), 0, 0, from.Location())
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}
