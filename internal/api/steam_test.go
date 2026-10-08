package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/config"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

const (
	testSteamID  = "76561198000000001"
	testPassword = "correct horse battery staple"
)

// freshNonce is a response_nonce as Steam makes them: the time, then something unique.
func freshNonce() string {
	return time.Now().UTC().Format(time.RFC3339) + rand.Text()
}

// fakeSteam plays Steam's OpenID provider: /login shows the "sign in" step
// by bouncing straight back to return_to with an assertion for testSteamID,
// and check_authentication confirms only assertions it issued. Pass the
// result to newPanel.
func fakeSteam(t *testing.T) func(*API) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			q := r.URL.Query()
			if q.Get("openid.mode") != "checkid_setup" || !strings.HasPrefix(q.Get("openid.return_to"), q.Get("openid.realm")) {
				http.Error(w, "bad checkid_setup", http.StatusBadRequest)
				return
			}
			id := "https://steamcommunity.com/openid/id/" + testSteamID
			back := url.Values{
				"openid.ns":             {"http://specs.openid.net/auth/2.0"},
				"openid.mode":           {"id_res"},
				"openid.op_endpoint":    {srv.URL},
				"openid.claimed_id":     {id},
				"openid.identity":       {id},
				"openid.return_to":      {q.Get("openid.return_to")},
				"openid.response_nonce": {freshNonce()},
				"openid.assoc_handle":   {"1234567890"},
				"openid.signed":         {"signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
				"openid.sig":            {"good"},
			}
			http.Redirect(w, r, q.Get("openid.return_to")+"&"+back.Encode(), http.StatusFound)
			return
		}
		_ = r.ParseForm()
		valid := r.PostForm.Get("openid.mode") == "check_authentication" && r.PostForm.Get("openid.sig") == "good"
		w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:" + map[bool]string{true: "true", false: "false"}[valid] + "\n"))
	}))
	t.Cleanup(srv.Close)
	return func(a *API) { a.Auth.Steam.Endpoint = srv.URL }
}

type panel struct {
	t      *testing.T
	url    string
	client *http.Client
	auth   *auth.Service
	store  *store.Store
	api    *API // set by newServersPanel
}

func newPanel(t *testing.T, setup ...func(*API)) *panel {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc, err := auth.New(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Setup(ctx, "127.0.0.1", svc.SetupPIN(), "owner", testPassword); err != nil {
		t.Fatal(err)
	}
	a := &API{Store: st, Auth: svc, Log: slog.New(slog.DiscardHandler), Static: fstest.MapFS{}}
	for _, f := range setup {
		f(a)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &panel{t: t, url: srv.URL, client: &http.Client{Jar: jar}, auth: svc, store: st}
}

// steam runs the whole browser round trip and returns where the panel sent
// the browser in the end.
func (p *panel) steam(mode string) *url.URL {
	p.t.Helper()
	p.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Follow the panel -> Steam -> panel hops, then stop at the panel's final answer.
		if strings.HasPrefix(req.URL.Path, "/api/auth/steam") || req.URL.Query().Get("openid.mode") != "" {
			return nil
		}
		return http.ErrUseLastResponse
	}
	defer func() { p.client.CheckRedirect = nil }()
	resp, err := p.client.Get(p.url + "/api/auth/steam?mode=" + mode)
	if err != nil {
		p.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		p.t.Fatalf("steam %s: status %d", mode, resp.StatusCode)
	}
	loc, err := resp.Location()
	if err != nil {
		p.t.Fatal(err)
	}
	return loc
}

func (p *panel) me() *auth.User {
	p.t.Helper()
	resp, err := p.client.Get(p.url + "/api/auth/me")
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ User *auth.User }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		p.t.Fatal(err)
	}
	return out.User
}

func (p *panel) post(path, body string) {
	p.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RSM", "1")
	resp, err := p.client.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("POST %s: %d %s", path, resp.StatusCode, b)
	}
}

func TestSteamLinkThenSignIn(t *testing.T) {
	p := newPanel(t, fakeSteam(t))

	// An unlinked Steam account has no panel access.
	if loc := p.steam("login"); loc.Path != "/login" || loc.Query().Get("error") != steamNoAccess {
		t.Fatalf("unlinked login went to %s", loc)
	}
	if p.me() != nil {
		t.Fatal("unlinked Steam account got a session")
	}

	// Linking needs a signed-in user.
	if loc := p.steam("link"); loc.Path != "/login" {
		t.Fatalf("anonymous link went to %s", loc)
	}

	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	if loc := p.steam("link"); loc.Path != "/account" || loc.Query().Get("linked") != "1" {
		t.Fatalf("link went to %s", loc)
	}
	if u := p.me(); u == nil || u.SteamID != testSteamID {
		t.Fatalf("after link: %+v", u)
	}

	p.post("/api/auth/logout", `{}`)
	if p.me() != nil {
		t.Fatal("still signed in after logout")
	}
	if loc := p.steam("login"); loc.Path != "/" || loc.RawQuery != "" {
		t.Fatalf("linked login went to %s", loc)
	}
	if u := p.me(); u == nil || u.Username != "owner" {
		t.Fatalf("after Steam sign-in: %+v", u)
	}
}

func TestSteamCallbackNeedsItsCookie(t *testing.T) {
	p := newPanel(t, fakeSteam(t))
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	// A callback with no rsm_steam cookie (expired, or started in another browser) is refused.
	resp, err := p.client.Get(p.url + "/api/auth/steam/callback?n=abc&openid.mode=id_res")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc, _ := resp.Location(); loc == nil || loc.Path != "/login" || loc.Query().Get("error") != steamExpired {
		t.Fatalf("cookieless callback went to %v", loc)
	}
}

// An assertion Steam made for another site cannot be handed in here by
// claiming to be that site in the Host header.
func TestSteamIgnoresTheHostHeader(t *testing.T) {
	var endpoint string
	p := newPanel(t, fakeSteam(t), func(a *API) { endpoint = a.Auth.Steam.Endpoint })
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	if loc := p.steam("link"); loc.Query().Get("linked") != "1" {
		t.Fatalf("link went to %s", loc)
	}
	p.post("/api/auth/logout", `{}`)

	// What the victim's browser brought back from signing in to evil.example.
	const evilReturn = "http://evil.example/api/auth/steam/callback?n=abc"
	id := "https://steamcommunity.com/openid/id/" + testSteamID
	q := url.Values{
		"n":                     {"abc"},
		"openid.ns":             {"http://specs.openid.net/auth/2.0"},
		"openid.mode":           {"id_res"},
		"openid.op_endpoint":    {endpoint},
		"openid.claimed_id":     {id},
		"openid.identity":       {id},
		"openid.return_to":      {evilReturn},
		"openid.response_nonce": {freshNonce()},
		"openid.assoc_handle":   {"1234567890"},
		"openid.signed":         {"signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
		"openid.sig":            {"good"},
	}
	req, _ := http.NewRequest(http.MethodGet, p.url+"/api/auth/steam/callback?"+q.Encode(), nil)
	req.Host = "evil.example"
	req.AddCookie(&http.Cookie{Name: steamCookie, Value: "abc.login"})
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc, _ := resp.Location(); loc == nil || loc.Query().Get("error") != steamNoAddress {
		t.Fatalf("callback under another Host went to %v", loc)
	}
	if p.me() != nil {
		t.Fatal("signed in with an assertion made for another site")
	}

	// Starting a sign-in there is refused too, rather than naming that site.
	req, _ = http.NewRequest(http.MethodGet, p.url+"/api/auth/steam?mode=login", nil)
	req.Host = "evil.example"
	resp, err = p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc, _ := resp.Location(); loc == nil || loc.Path != "/login" || loc.Query().Get("error") != steamNoAddress {
		t.Fatalf("start under another Host went to %v", loc)
	}
}

func TestSteamUsesThePublicAddress(t *testing.T) {
	p := newPanel(t, func(a *API) { a.Cfg.PublicURL = "https://panel.example.com" })
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest(http.MethodGet, p.url+"/api/auth/steam?mode=login", nil)
	req.Host = "evil.example"
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := resp.Location()
	if loc == nil || !strings.HasPrefix(loc.Query().Get("openid.return_to"), "https://panel.example.com/api/auth/steam/callback?n=") ||
		loc.Query().Get("openid.realm") != "https://panel.example.com" {
		t.Fatalf("sign-in sent to %v", loc)
	}
}

func TestSteamAvailability(t *testing.T) {
	for _, c := range []struct {
		public, host string
		want         bool
	}{
		{"", "127.0.0.1:40125", true},
		{"", "localhost:40125", true},
		{"", "[::1]:40125", true},
		{"", "192.168.1.10:40125", false},
		{"", "panel.example.com", false},
		{"https://panel.example.com", "192.168.1.10:40125", true},
	} {
		a := &API{Cfg: config.Config{PublicURL: c.public}}
		r := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		r.Host = c.host
		if _, got := a.steamBase(r); got != c.want {
			t.Errorf("public %q, host %q: %v", c.public, c.host, got)
		}
	}
}

func TestLoginLimited(t *testing.T) {
	p := newPanel(t, func(a *API) { a.Cfg.Proxy = config.ProxyForwarded })
	try := func(ip, password string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, p.url+"/api/auth/login", strings.NewReader(`{"username":"owner","password":"`+password+`"}`))
		req.Header.Set("X-RSM", "1")
		req.Header.Set("X-Forwarded-For", ip)
		resp, err := p.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for i := range 10 {
		if resp := try("198.51.100.7", "wrong"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, resp.StatusCode)
		}
	}
	resp := try("198.51.100.7", testPassword)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("11th try: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp := try("192.0.2.1", testPassword); resp.StatusCode != http.StatusOK {
		t.Fatalf("from elsewhere: %d", resp.StatusCode)
	}

	// Refused tries aren't written down; failed ones are, their name clipped.
	try("192.0.2.2", "wrong")
	req, _ := http.NewRequest(http.MethodPost, p.url+"/api/auth/login",
		strings.NewReader(`{"username":"`+strings.Repeat("x", 5000)+`","password":"wrong"}`))
	req.Header.Set("X-RSM", "1")
	req.Header.Set("X-Forwarded-For", "192.0.2.3")
	if resp, err := p.client.Do(req); err == nil {
		resp.Body.Close()
	}
	entries, err := p.store.AuditLog(context.Background(), "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, e := range entries {
		if e.Action == "auth.login.failed" {
			failed++
			if len(e.Detail) > 64 {
				t.Errorf("failed sign-in kept as %d bytes", len(e.Detail))
			}
		}
	}
	if failed != 12 {
		t.Errorf("%d failed sign-ins written down, want 12", failed)
	}
}
