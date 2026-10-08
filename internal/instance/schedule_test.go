package instance

import (
	"testing"
	"time"
)

func TestParseRestartTimes(t *testing.T) {
	got, err := ParseRestartTimes([]string{"16:30", " 4:00", "04:00"})
	if err != nil || len(got) != 2 || got[0] != "04:00" || got[1] != "16:30" {
		t.Fatalf("%v, %v", got, err)
	}
	for _, bad := range [][]string{{"24:00"}, {"4pm"}, {""}, {"1:00", "2:00", "3:00", "4:00", "5:00", "6:00", "7:00"}} {
		if _, err := ParseRestartTimes(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func TestNextRestart(t *testing.T) {
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		what           string
		d              Def
		started, after time.Time
		want           time.Time
	}{
		{"no schedule", Def{}, at(5, 3, 0), at(5, 3, 0), time.Time{}},
		{"later today", Def{RestartTimes: []string{"04:00"}}, at(5, 1, 0), at(5, 1, 0), at(5, 4, 0)},
		{"tomorrow once today's has gone", Def{RestartTimes: []string{"04:00"}}, at(5, 1, 0), at(5, 4, 0), at(6, 4, 0)},
		{"too soon after the start", Def{RestartTimes: []string{"04:00"}}, at(5, 3, 55), at(5, 3, 55), at(6, 4, 0)},
		{"the earliest of several", Def{RestartTimes: []string{"04:00", "16:00"}}, at(5, 5, 0), at(5, 5, 0), at(5, 16, 0)},
		{"hours of uptime", Def{RestartHours: 6}, at(5, 1, 0), at(5, 1, 0), at(5, 7, 0)},
		{"hours, the one after", Def{RestartHours: 6}, at(5, 1, 0), at(5, 7, 0), at(5, 13, 0)},
		{"hours, a while later", Def{RestartHours: 6}, at(5, 1, 0), at(5, 14, 0), at(5, 19, 0)},
		{"whichever comes first", Def{RestartTimes: []string{"04:00"}, RestartHours: 2}, at(5, 1, 0), at(5, 1, 0), at(5, 3, 0)},
	} {
		if got := c.d.NextRestart(c.started, c.after); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.what, got, c.want)
		}
	}
}
