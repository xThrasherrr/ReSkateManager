package announce

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
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

// An announcement goes out with announce, a card too, on a server that has
// it, and with say on one that doesn't.
func TestPost(t *testing.T) {
	ctx := context.Background()
	def := instance.Def{ID: "srv", Name: "Srv", Dir: t.TempDir()}
	if out, err := exec.Command("go", "build", "-o", def.Exe(), "../instance/testdata/fakeserver").CombinedOutput(); err != nil {
		t.Fatalf("build fake server: %v: %s", err, out)
	}
	in := instance.New(def, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := in.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in.Stop(ctx)
	})
	for deadline := time.Now().Add(10 * time.Second); in.State() != instance.Running; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("state %s", in.State())
		}
	}

	if reply, err := Post(ctx, in, "hi", "test"); err != nil || reply != "[chat] Server: hi" {
		t.Errorf("before 2.0.2: %q, %v", reply, err)
	}
	// 2.0.2 writes its announcements, and logs the card's line before the reply.
	if err := os.WriteFile(def.ConfigPath(), []byte(`{"announcements": {"messages": [], "interval_minutes": 0, "card": true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if reply, err := Post(ctx, in, "hello", "test"); err != nil || reply != "Announced." {
		t.Errorf("2.0.2: %q, %v", reply, err)
	}
	found := false
	for _, e := range in.Console() {
		found = found || e.Kind == logparse.KindTagged && e.Tag == "announcement" && e.Text == "[announcement] hello"
	}
	if !found {
		t.Error("no [announcement] line in the console")
	}
	// Taken back to an older release, under the file 2.0.2 wrote.
	if err := os.WriteFile(filepath.Join(def.Dir, "before-2.0.2"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if reply, err := Post(ctx, in, "again", "test"); err != nil || reply != "[chat] Server: again" {
		t.Errorf("older again: %q, %v", reply, err)
	}
}
