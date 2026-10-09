package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/discord"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

func TestManagerSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.toml")
	var api *API
	p := newPanel(t, func(a *API) { a.ConfigPath, api = path, a })
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)

	code, b := p.do("PATCH", "/api/manager/settings", `{"proxy":"cloudflare","publicUrl":"https://Panel.Example.com/"}`)
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, b)
	}
	var got accessSettings
	json.Unmarshal(b, &got)
	if got.Proxy != config.ProxyCloudflare || got.PublicURL != "https://panel.example.com" {
		t.Errorf("saved %+v", got)
	}
	// In effect at once, and written to the file for the next start.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:50000" // a Cloudflare Tunnel on this machine
	r.Header.Set("Cf-Connecting-IP", "198.51.100.4")
	if ip := api.clientIP(r); ip != "198.51.100.4" {
		t.Errorf("clientIP after save = %q", ip)
	}
	c, err := config.Load(path)
	if err != nil || c.Proxy != config.ProxyCloudflare || c.PublicURL != "https://panel.example.com" {
		t.Errorf("file holds %q %q (%v)", c.Proxy, c.PublicURL, err)
	}

	for _, body := range []string{`{"proxy":"nginx"}`, `{"publicUrl":"panel.example.com"}`, `{"publicUrl":"https://x.com/panel"}`} {
		if code, _ := p.do("PATCH", "/api/manager/settings", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}

	p.createUser("helper")
	p.post("/api/auth/logout", "")
	p.post("/api/auth/login", `{"username":"helper","password":"`+testPassword+`"}`)
	if code, _ := p.do("GET", "/api/manager/settings", ""); code != http.StatusForbidden {
		t.Errorf("non-owner read: %d, want 403", code)
	}
	if code, _ := p.do("PATCH", "/api/manager/settings", `{"proxy":""}`); code != http.StatusForbidden {
		t.Errorf("non-owner save: %d, want 403", code)
	}
}

func TestManagerSettingsLockedByEnv(t *testing.T) {
	t.Setenv("RSM_PROXY", "cloudflare")
	p := newPanel(t, func(a *API) {
		a.ConfigPath = filepath.Join(t.TempDir(), "manager.toml")
		a.Cfg.Proxy = config.ProxyCloudflare
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	_, b := p.do("GET", "/api/manager/settings", "")
	var got accessSettings
	json.Unmarshal(b, &got)
	if len(got.Locked) != 1 || got.Locked[0] != "proxy" {
		t.Errorf("locked = %v, want [proxy]", got.Locked)
	}
	if code, _ := p.do("PATCH", "/api/manager/settings", `{"proxy":"forwarded"}`); code != http.StatusBadRequest {
		t.Errorf("changing a locked setting: %d, want 400", code)
	}
	if code, b := p.do("PATCH", "/api/manager/settings", `{"publicUrl":"https://panel.example.com"}`); code != http.StatusOK {
		t.Errorf("unlocked setting: %d %s", code, b)
	}
}

// Owners set up the Discord status message. The bot token is saved but never
// sent back, and moving the message deletes the old one.
func TestManagerDiscord(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			io.WriteString(w, `{"name":"status","guild_id":"222222222222222222"}`)
		case http.MethodPost, http.MethodPatch:
			io.WriteString(w, `{"id":"900000000000000001","embeds":[{}]}`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(fake.Close)
	took := func() string {
		mu.Lock()
		defer mu.Unlock()
		c := strings.Join(calls, ", ")
		calls = nil
		return c
	}
	p := newPanel(t, func(a *API) {
		log := slog.New(slog.DiscardHandler)
		a.Discord = &discord.Poster{Store: a.Store, Reg: instance.NewRegistry(log), Log: log, API: fake.URL}
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	var got discordView
	code, b := p.do("GET", "/api/manager/discord", "")
	if json.Unmarshal(b, &got); code != http.StatusOK || got.TokenSet || got.Channel != "" || !got.JoinCodes {
		t.Fatalf("nothing set: %d %s", code, b)
	}

	token := base64.RawStdEncoding.EncodeToString([]byte("1045678901234567890")) + ".GabcDE.abcdefghijklmnopqrstuvwxyz0123"
	for _, body := range []string{`{"token":"not-a-token"}`, `{"channel":"#general"}`, `{"title":"` + strings.Repeat("x", 257) + `"}`} {
		if code, b := p.do("PATCH", "/api/manager/discord", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, code, b)
		}
	}
	if code, b := p.do("POST", "/api/manager/discord/test", `{}`); code != http.StatusBadRequest {
		t.Errorf("test without a token: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/manager/discord/test", `{"token":"`+token+`","channel":"111111111111111111"}`); code != http.StatusOK || !strings.Contains(string(b), `"status"`) {
		t.Errorf("test: %d %s", code, b)
	}
	took()

	code, b = p.do("PATCH", "/api/manager/discord", `{"token":" Bot `+token+` ","channel":"https://discord.com/channels/222222222222222222/111111111111111111","joinCodes":false,"title":"  baked.  servers "}`)
	if json.Unmarshal(b, &got); code != http.StatusOK || !got.TokenSet || got.Channel != "111111111111111111" || got.JoinCodes || got.Title != "baked. servers" ||
		got.Status.Channel != "status" || got.Status.Problem != "" || !strings.Contains(got.Invite, "client_id=1045678901234567890") {
		t.Fatalf("save: %d %s", code, b)
	}
	if strings.Contains(string(b), token) {
		t.Errorf("the token was sent back: %s", b)
	}
	if c := took(); c != "GET /channels/111111111111111111, POST /channels/111111111111111111/messages" {
		t.Errorf("posted with %s", c)
	}
	if _, b := p.do("GET", "/api/manager/discord", ""); strings.Contains(string(b), token) {
		t.Errorf("the token was sent back: %s", b)
	}

	// A reset token is the same bot: it goes on editing the same message.
	reset := strings.Split(token, ".")[0] + ".HxyzAB.zyxwvutsrqponmlkjihgfedcba9876"
	if code, b := p.do("PATCH", "/api/manager/discord", `{"token":"`+reset+`"}`); code != http.StatusOK {
		t.Fatalf("reset token: %d %s", code, b)
	}
	if c := took(); c != "PATCH /channels/111111111111111111/messages/900000000000000001" {
		t.Errorf("reset token: %s", c)
	}

	// Moving to another channel deletes the message in the old one.
	if code, b := p.do("PATCH", "/api/manager/discord", `{"channel":"333333333333333333"}`); code != http.StatusOK {
		t.Fatalf("move: %d %s", code, b)
	}
	if c := took(); !strings.HasPrefix(c, "DELETE /channels/111111111111111111/messages/900000000000000001, GET /channels/333333333333333333") {
		t.Errorf("moved with %s", c)
	}
	// Removing the token turns it off; the channel stays for next time.
	code, b = p.do("PATCH", "/api/manager/discord", `{"token":""}`)
	if got = (discordView{}); json.Unmarshal(b, &got) != nil || code != http.StatusOK || got.TokenSet || got.Channel != "333333333333333333" || got.Status != (discord.Status{}) {
		t.Errorf("removed: %d %s", code, b)
	}
	if _, b := p.do("GET", "/api/audit", ""); !strings.Contains(string(b), `"manager.discord"`) || !strings.Contains(string(b), `"manager.discord.test"`) {
		t.Errorf("audit: %s", b)
	}

	p.createUser("helper")
	p.post("/api/auth/logout", "")
	p.post("/api/auth/login", `{"username":"helper","password":"`+testPassword+`"}`)
	if code, _ := p.do("GET", "/api/manager/discord", ""); code != http.StatusForbidden {
		t.Errorf("non-owner read: %d, want 403", code)
	}
}
