package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/plugins"
)

func TestPlugins(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "hello", "plugin.toml"), []byte("api = 0\nname = \"Hello\"\nversion = \"0.1.0\""), 0o644)
	os.WriteFile(filepath.Join(dir, "hello", "main.lua"), []byte(`log("hi")`), 0o644)
	p := newPanel(t, func(a *API) {
		a.Plugins = &plugins.Manager{Dir: dir, Store: a.Store, Log: a.Log, LogPath: filepath.Join(dir, "plugins.log")}
		if err := a.Plugins.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Plugins.Close)
	})
	p.signIn("owner")
	list := func() pluginsInfo {
		t.Helper()
		code, b := p.do("GET", "/api/manager/plugins", "")
		var info pluginsInfo
		if err := json.Unmarshal(b, &info); code != http.StatusOK || err != nil {
			t.Fatalf("list: %d %s", code, b)
		}
		return info
	}
	info := list()
	if info.API != 0 || info.CallMs != 100 || info.MemoryMB != 64 || len(info.Plugins) != 1 || info.Plugins[0].Name != "Hello" || info.Plugins[0].State != plugins.Off {
		t.Fatalf("list %+v", info)
	}
	if code, b := p.do("PATCH", "/api/manager/plugins/hello", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("enable: %d %s", code, b)
	}
	for deadline := time.Now().Add(5 * time.Second); list().Plugins[0].State != plugins.Running; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never ran: %+v", list().Plugins[0])
		}
	}
	if s := list().Plugins[0]; !s.Enabled || len(s.Recent) != 1 || s.Recent[0].Text != "hi" {
		t.Fatalf("running %+v", s)
	}
	if code, b := p.do("POST", "/api/manager/plugins/hello/reload", ""); code != http.StatusOK {
		t.Fatalf("reload: %d %s", code, b)
	}
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"PATCH", "/api/manager/plugins/nope", `{"enabled":true}`, http.StatusNotFound},
		{"POST", "/api/manager/plugins/nope/reload", "", http.StatusNotFound},
		{"PATCH", "/api/manager/plugins/hello", `{}`, http.StatusBadRequest},
	} {
		if code, b := p.do(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, code, b, c.want)
		}
	}
	_, b := p.do("GET", "/api/audit", "")
	var entries []struct{ Action, Detail, Username string }
	json.Unmarshal(b, &entries)
	var saw []string
	for _, e := range entries {
		if e.Action == "manager.plugins" && e.Username == "owner" {
			saw = append(saw, e.Detail)
		}
	}
	slices.Sort(saw)
	if !slices.Equal(saw, []string{"reloaded hello", "turned on hello"}) {
		t.Fatalf("audited %q", saw)
	}

	off := newPanel(t)
	off.signIn("owner")
	if code, _ := off.do("GET", "/api/manager/plugins", ""); code != http.StatusNotFound {
		t.Fatalf("without plugins: %d", code)
	}
}
