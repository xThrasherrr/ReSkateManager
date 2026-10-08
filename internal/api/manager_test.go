package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/config"
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
