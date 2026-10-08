package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// A user's uploads left behind make way for a new one; ones under way don't.
func TestUploadsPerUser(t *testing.T) {
	a := &API{modUploads: map[string]*modUpload{}}
	for i := range uploadsPerUser {
		id := fmt.Sprint(i)
		a.modUploads[id] = &modUpload{id: id, user: 1, touched: time.Now()}
	}
	if err := a.roomForUpload(1); err == nil {
		t.Fatal("a fifth upload while four are under way")
	}
	if err := a.roomForUpload(2); err != nil {
		t.Fatalf("someone else: %v", err)
	}
	a.modUploads["2"].touched = time.Now().Add(-5 * time.Minute)
	if err := a.roomForUpload(1); err != nil {
		t.Fatalf("with one left behind: %v", err)
	}
	if _, ok := a.modUploads["2"]; ok || len(a.modUploads) != uploadsPerUser-1 {
		t.Fatalf("the one left behind wasn't dropped: %v", a.modUploads)
	}
	for i := range uploadsAll {
		id := fmt.Sprint("other", i)
		a.modUploads[id] = &modUpload{id: id, user: int64(100 + i), touched: time.Now()}
	}
	if err := a.roomForUpload(3); err == nil {
		t.Fatal("an upload past the manager's limit")
	}
}

func TestSocketsPerUser(t *testing.T) {
	a := &API{}
	for range socketsPerUser {
		if !a.openSocket(1) {
			t.Fatal("refused under the limit")
		}
	}
	if a.openSocket(1) {
		t.Fatal("one socket past the limit")
	}
	if !a.openSocket(2) {
		t.Fatal("another user refused")
	}
	a.closeSocket(1)
	if !a.openSocket(1) {
		t.Fatal("refused after one closed")
	}
}

func TestLogQueryLimits(t *testing.T) {
	for query, ok := range map[string]bool{
		"from=10&to=20":                    true,
		"from=20&to=10":                    false,
		"from=9300000000000000":            false, // past what milliseconds hold
		"q=" + strings.Repeat("x", 200):    true,
		"q=" + strings.Repeat("x", 201):    false,
		"q=" + strings.Repeat("é", 200):    true,
		"source=console&group=chat&from=1": true,
	} {
		r := httptest.NewRequest(http.MethodGet, "/logs?"+query, nil)
		if _, err := parseLogQuery(r); (err == nil) != ok {
			t.Errorf("%s: %v", query, err)
		}
	}
}

func TestAnnouncementsPerServer(t *testing.T) {
	var st *store.Store
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
		a.ServersDir = t.TempDir()
		st = a.Store
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	for _, name := range []string{"a", "b"} {
		if code, b := p.do("POST", "/api/instances", `{"name":"`+name+`"}`); code != 201 {
			t.Fatalf("create %s: %d %s", name, code, b)
		}
	}
	for i := range maxAnnouncements {
		ann := store.Announcement{Instance: "a", Message: fmt.Sprint("hello ", i), Interval: 3600}
		if err := st.SaveAnnouncement(context.Background(), &ann); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"message":"one more","interval":3600,"enabled":true}`
	if code, b := p.do("POST", "/api/instances/a/announcements", body); code != http.StatusConflict {
		t.Errorf("past the limit on a: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/instances/b/announcements", body); code != 200 {
		t.Errorf("on b: %d %s", code, b)
	}
	// Every server's goes to a too, which has no room.
	all := `{"allServers":true,"message":"to all","interval":3600,"enabled":true}`
	if code, b := p.do("POST", "/api/instances/b/announcements", all); code != http.StatusConflict {
		t.Errorf("every server's, with a full: %d %s", code, b)
	}
}

// Background work counts as busy until it ends, and Wait waits for it.
func TestBackgroundWork(t *testing.T) {
	a := &API{}
	release := make(chan struct{})
	a.goWork(func(context.Context) { <-release })
	if !a.Busy() || a.Wait(20*time.Millisecond) {
		t.Fatal("work under way isn't busy")
	}
	close(release)
	if !a.Wait(5*time.Second) || a.Busy() {
		t.Fatal("finished work is still busy")
	}
}
