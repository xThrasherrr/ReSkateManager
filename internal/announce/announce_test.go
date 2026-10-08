package announce

import (
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

func TestFor(t *testing.T) {
	all := []store.Announcement{
		{ID: 1, Instance: "a", Enabled: true},
		{ID: 2, Instance: store.AllInstances, Enabled: true},
		{ID: 3, Instance: "b", Enabled: true},
		{ID: 4, Instance: "a", Enabled: false},
	}
	got := For(all, "a")
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("For(a) = %+v", got)
	}
}

func TestClockWaitsOneInterval(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	list := []store.Announcement{{ID: 1, Interval: 600, Message: "hi"}}
	var c Clock
	if _, ok := c.Due(list, true, t0); ok {
		t.Fatal("sent before its first interval")
	}
	if _, ok := c.Due(list, true, t0.Add(599*time.Second)); ok {
		t.Fatal("sent early")
	}
	if a, ok := c.Due(list, true, t0.Add(600*time.Second)); !ok || a.ID != 1 {
		t.Fatal("not sent when due")
	}
	if _, ok := c.Due(list, true, t0.Add(900*time.Second)); ok {
		t.Fatal("sent again before the next interval")
	}
	if _, ok := c.Due(list, true, t0.Add(1200*time.Second)); !ok {
		t.Fatal("not sent on the next interval")
	}
}

func TestClockRestartsWhenEmpty(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	list := []store.Announcement{{ID: 1, Interval: 600}}
	var c Clock
	c.Due(list, true, t0)
	c.Due(list, false, t0.Add(500*time.Second)) // everyone left
	if _, ok := c.Due(list, true, t0.Add(700*time.Second)); ok {
		t.Fatal("sent right after the server filled again")
	}
	if _, ok := c.Due(list, true, t0.Add(1100*time.Second)); !ok {
		t.Fatal("not sent one interval after emptying")
	}
}

func TestClockSpacesAnnouncements(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	list := []store.Announcement{{ID: 1, Interval: 600}, {ID: 2, Interval: 600}}
	var c Clock
	c.Due(list, true, t0)
	a, ok := c.Due(list, true, t0.Add(600*time.Second))
	if !ok || a.ID != 1 {
		t.Fatalf("first due = %+v %v", a, ok)
	}
	if _, ok := c.Due(list, true, t0.Add(630*time.Second)); ok {
		t.Fatal("second sent inside the gap")
	}
	if a, ok := c.Due(list, true, t0.Add(660*time.Second)); !ok || a.ID != 2 {
		t.Fatalf("second after the gap = %+v %v", a, ok)
	}
}

func TestClockForgetsRemoved(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	var c Clock
	c.Due([]store.Announcement{{ID: 1, Interval: 60}}, true, t0)
	c.Due(nil, true, t0.Add(time.Second))
	// Re-added (or re-enabled): it waits a fresh interval.
	if _, ok := c.Due([]store.Announcement{{ID: 1, Interval: 60}}, true, t0.Add(90*time.Second)); ok {
		t.Fatal("re-added announcement sent at once")
	}
}

func TestValidInterval(t *testing.T) {
	for seconds, ok := range map[int]bool{
		59:                false,
		60:                true,
		7 * 24 * 3600:     true,
		7*24*3600 + 1:     false,
		-60:               false,
		36028797018967568: false, // 3600 + 2^55: as a Duration, it wraps to an hour
	} {
		if ValidInterval(seconds) != ok {
			t.Errorf("ValidInterval(%d) = %v", seconds, !ok)
		}
	}
}
