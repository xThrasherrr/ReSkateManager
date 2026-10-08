package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

func TestReadJSONIsStrict(t *testing.T) {
	p := newPanel(t)
	for body, want := range map[string]int{
		`{"username":"owner","password":"` + testPassword + `"}`:                 200,
		`{"username":"owner","password":"` + testPassword + `"} trailing`:        400,
		`{"username":"owner","password":"` + testPassword + `"}{"username":"x"}`: 400,
		`{"username":"owner","password":"x","admin":true}`:                       400,
		`{"username":"` + strings.Repeat("x", 2<<20) + `"}`:                      413,
	} {
		if code, _ := p.do("POST", "/api/auth/login", body); code != want {
			t.Errorf("%.60s: %d, want %d", body, code, want)
		}
	}
}

func TestServerName(t *testing.T) {
	for in, ok := range map[string]bool{
		"Lobby":                 true,
		"  Lobby  ":             true,
		"Ünïcödé [EU] / 24/7 🛹": true,
		strings.Repeat("é", 64): true, // characters, not bytes
		strings.Repeat("é", 65): false,
		"":                      false,
		"   ":                   false,
		"tab\there":             false,
		"evil\u202egnp.exe":     false, // right-to-left override
		"isolate\u2066x\u2069":  false,
		"mark\u061cx":           false, // Arabic letter mark, a direction character too
		"bad \xff":              false,
	} {
		if _, err := serverName(in); (err == nil) != ok {
			t.Errorf("serverName(%q) = %v", in, err)
		}
	}
}

// What the system said (paths, SQL) reaches owners, who run the machine, and
// no one else.
func TestFailHidesSystemErrors(t *testing.T) {
	a := &API{Log: slog.New(slog.DiscardHandler)}
	sys := &fs.PathError{Op: "open", Path: `C:\secret\manager.db`, Err: os.ErrPermission}
	for _, c := range []struct {
		owner bool
		err   error
		shows bool
	}{
		{true, sys, true},
		{false, sys, false},
		{false, errors.New("names are 1-64 characters"), true},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, &caller{user: &auth.User{}, perms: auth.Perms{Owner: c.owner}}))
		w := httptest.NewRecorder()
		a.fail(w, r, 400, c.err)
		var got struct{ Error string }
		json.Unmarshal(w.Body.Bytes(), &got)
		if (got.Error == c.err.Error()) != c.shows {
			t.Errorf("owner %v, %v: answered %d %s", c.owner, c.err, w.Code, w.Body)
		}
	}
	if got := a.failText(false, sys); strings.Contains(got, "secret") {
		t.Errorf("a job told someone else %q", got)
	}
}

func TestWholeDownload(t *testing.T) {
	for rng, whole := range map[string]bool{
		"":                true,
		"bytes=0-":        true,
		"bytes=0-99":      true,
		"bytes=100-":      false,
		"bytes=1-, 0-5":   true, // several ranges can include the start
		"bytes=-500":      false,
		"  bytes=0-100  ": true,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if rng != "" {
			r.Header.Set("Range", rng)
		}
		if got := wholeDownload(r); got != whole {
			t.Errorf("Range %q: %v", rng, got)
		}
	}
}

func TestAttachment(t *testing.T) {
	w := httptest.NewRecorder()
	attachment(w, `a "quoted"; name.log`)
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="a \"quoted\"; name.log"` {
		t.Errorf("header %s", got)
	}
}

// A folder named with a % can be managed: its name isn't decoded twice.
func TestModFolderWithPercent(t *testing.T) {
	p, dir := newModsPanel(t, nil)
	if err := os.MkdirAll(filepath.Join(dir, "Mods", "50%"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, b := p.do("PATCH", "/api/instances/a/mods/50%25", `{"enabled":false}`); code != 200 {
		t.Fatalf("disable 50%%: %d %s", code, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "DisabledMods", "50%")); err != nil {
		t.Fatal(err)
	}
}

// An import holds its ID, so a server made meanwhile doesn't take its folder.
func TestReservedIDs(t *testing.T) {
	var a *API
	p := newPanel(t, func(x *API) {
		x.Reg = instance.NewRegistry(x.Log)
		x.ServersDir = t.TempDir()
		a = x
	})
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	id, err := a.reserveID("Lobby")
	if err != nil || id != "lobby" {
		t.Fatalf("reserve: %q %v", id, err)
	}
	code, b := p.do("POST", "/api/instances", `{"name":"Lobby"}`)
	if code != 201 || !strings.Contains(string(b), `"id":"lobby-2"`) {
		t.Fatalf("create while reserved: %d %s", code, b)
	}
	a.releaseID(id)
	if again, _ := a.reserveID("Lobby"); again != "lobby" {
		t.Errorf("after release: %q", again)
	}
}
