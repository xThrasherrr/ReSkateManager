package alerts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

func TestCheckWebhook(t *testing.T) {
	for _, ok := range []string{"", "https://discord.com/api/webhooks/123/abc-DEF_9", "https://canary.discord.com/api/v10/webhooks/1/x", "https://discordapp.com/api/webhooks/1/x"} {
		if err := CheckWebhook(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://discord.com/api/webhooks/1/x", "https://evil.com/api/webhooks/1/x", "https://discord.com.evil.com/api/webhooks/1/x",
		"https://discord.com/api/webhooks/1/x?wait=true", "https://discord.com/api/webhooks/abc/x"} {
		if err := CheckWebhook(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if set, err := Load(ctx, s); err != nil || set.Webhook != "" || len(set.Off) != 0 {
		t.Fatalf("nothing saved: %+v, %v", set, err)
	}
	if set, _ := Load(ctx, s); set.DiskGB != 10 || set.MemPct != 90 || set.MemMinutes != 10 {
		t.Fatalf("default thresholds: %+v", set)
	}
	want := Settings{Webhook: "https://discord.com/api/webhooks/1/x", Off: []string{Updated}, DiskGB: 5, MemPct: 95, MemMinutes: 3}
	if err := Save(ctx, s, want); err != nil {
		t.Fatal(err)
	}
	if set, _ := Load(ctx, s); set.Webhook != want.Webhook || len(set.Off) != 1 || set.Off[0] != Updated || set.DiskGB != 5 || set.MemPct != 95 || set.MemMinutes != 3 {
		t.Errorf("loaded %+v", set)
	}
	// Settings saved before the thresholds existed load with the defaults.
	s.Set(ctx, settingsKey, `{"webhook":"","off":["crash"]}`)
	if set, _ := Load(ctx, s); set.DiskGB != 10 || set.MemPct != 90 || set.MemMinutes != 10 || set.Off[0] != Crash {
		t.Errorf("older settings: %+v", set)
	}
	for name, change := range map[string]func(*Settings){
		"an unknown kind":            func(s *Settings) { s.Off = []string{"everything"} },
		"a webhook not on Discord":   func(s *Settings) { s.Webhook = "https://example.com/hook" },
		"no free space":              func(s *Settings) { s.DiskGB = 0 },
		"all of the memory":          func(s *Settings) { s.MemPct = 100 },
		"too little memory":          func(s *Settings) { s.MemPct = 20 },
		"memory high for no time":    func(s *Settings) { s.MemMinutes = 0 },
		"memory high for over a day": func(s *Settings) { s.MemMinutes = 1441 },
	} {
		set := Defaults
		change(&set)
		if err := Save(ctx, s, set); err == nil {
			t.Errorf("saved %s: %+v", name, set)
		}
	}
}

// Alerts go out in order to the webhook, as an embed that pings nobody,
// except the kinds turned off. A rate limit waits and tries again.
func TestDelivery(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	limited := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !limited {
			limited = true
			w.Header().Set("Retry-After", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"retry_after":0.05}`)
			return
		}
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := openStore(t)
	// Save refuses a webhook not on Discord, so the test server's goes in directly.
	b, _ := json.Marshal(Settings{Webhook: srv.URL + "/api/webhooks/1/x", Off: []string{Updated}})
	s.Set(ctx, settingsKey, string(b))
	n := &Notifier{Store: s, Log: slog.New(slog.DiscardHandler), Version: "1.2.3"}
	n.Start(ctx)

	n.Send(ForCrash("Lobby", "https://panel.example.com/s/lobby", instance.Crash{Code: 3, Reason: "Bob said @everyone ```hi```", Retry: 5 * time.Second, Attempt: 1, Of: 5}))
	n.Send(ForUpdate("Lobby", "", "1.4", "auto-update", nil)) // turned off
	n.Send(ForManagerUpdate("0.4.0", "0.5.0", ""))
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		count := len(got)
		mu.Unlock()
		if count == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("delivered %d alerts, want 2: %v", len(got), got)
	}
	embed := got[0]["embeds"].([]any)[0].(map[string]any)
	desc := embed["description"].(string)
	if embed["title"] != "Lobby crashed" || embed["url"] != "https://panel.example.com/s/lobby" || !strings.Contains(desc, "try 1 of 5") ||
		!strings.Contains(desc, "\n```\nBob said @everyone ˋˋˋhiˋˋˋ\n```") || strings.Count(desc, "```") != 2 {
		t.Errorf("crash alert: %v", embed)
	}
	if parse := got[0]["allowed_mentions"].(map[string]any)["parse"].([]any); len(parse) != 0 {
		t.Errorf("mentions allowed: %v", parse)
	}
	if title := got[1]["embeds"].([]any)[0].(map[string]any)["title"]; title != "ReSkateManager updated to 0.5.0" {
		t.Errorf("second alert %v", title)
	}
}

// The test button hears Discord's own error.
func TestPostReportsDiscordErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message": "Unknown Webhook", "code": 10015}`)
	}))
	t.Cleanup(srv.Close)
	n := &Notifier{Log: slog.New(slog.DiscardHandler)}
	err := n.Post(context.Background(), srv.URL, Alert{Kind: Test, Title: "Test"})
	if err == nil || !strings.Contains(err.Error(), "Unknown Webhook") {
		t.Errorf("err %v", err)
	}
}

func TestForCrash(t *testing.T) {
	for _, c := range []struct {
		crash       instance.Crash
		kind, title string
	}{
		{instance.Crash{Code: 1, Retry: time.Second, Attempt: 2, Of: 5}, Crash, "L crashed"},
		{instance.Crash{Code: 1, GaveUp: true, Of: 5}, GaveUp, "L keeps crashing"},
		{instance.Crash{Code: 1, Fatal: true}, Stopped, "L stopped"},
		{instance.Crash{Code: -1, NoStart: true}, Stopped, "L could not restart"},
		{instance.Crash{Code: 0}, Stopped, "L stopped"},
	} {
		if a := ForCrash("L", "", c.crash); a.Kind != c.kind || a.Title != c.title || a.Text == "" {
			t.Errorf("%+v: %+v", c.crash, a)
		}
	}
}

// A webhook's address holds its token, so an error never repeats it, and a
// redirect isn't followed anywhere with the alert.
func TestPostKeepsTheToken(t *testing.T) {
	n := &Notifier{Log: slog.New(slog.DiscardHandler)}
	err := n.Post(context.Background(), "http://127.0.0.1:1/api/webhooks/1/SECRET-TOKEN", Alert{Kind: Test, Title: "Test"})
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Errorf("err %v", err)
	}

	var elsewhere bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere = true }))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	if err := n.Post(context.Background(), srv.URL+"/api/webhooks/1/x", Alert{Kind: Test, Title: "Test"}); err == nil || elsewhere {
		t.Errorf("redirected: err %v, followed %v", err, elsewhere)
	}
}
