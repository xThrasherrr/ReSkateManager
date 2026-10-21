package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/plugins"
)

func TestPlugins(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hello"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "hello", "plugin.toml"), []byte("api = 0\nname = \"Hello\"\nversion = \"0.1.0\"\npermissions = [\"players.chat\", \"audit.view\"]"), 0o644)
	os.WriteFile(filepath.Join(dir, "hello", "main.lua"), []byte(`log("hi")`), 0o644)
	p := newPanel(t, func(a *API) {
		a.Reg = instance.NewRegistry(a.Log)
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
	if info.API != 0 || info.CallMs != 100 || info.MemoryMB != 64 || len(info.Plugins) != 1 || info.Plugins[0].Name != "Hello" || info.Plugins[0].State != plugins.Off ||
		len(info.Grants) != len(plugins.Grants) || len(info.Plugins[0].Granted) != 0 {
		t.Fatalf("list %+v", info)
	}
	code, b := p.do("PATCH", "/api/manager/plugins/hello", `{"granted":["players.chat"]}`)
	var s plugins.Status
	if err := json.Unmarshal(b, &s); code != http.StatusOK || err != nil || !slices.Equal(s.Granted, []string{"players.chat"}) || s.Enabled {
		t.Fatalf("grant: %d %s", code, b)
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
		{"PATCH", "/api/manager/plugins/hello", `{"granted":["audit.view"]}`, http.StatusBadRequest},
		{"PATCH", "/api/manager/plugins/hello", `{"granted":["players.ban"]}`, http.StatusBadRequest},
		{"PATCH", "/api/manager/plugins/nope", `{"granted":[]}`, http.StatusNotFound},
		{"GET", "/api/boards?period=week&limit=100", "", http.StatusOK},
		{"GET", "/api/boards?server=nope", "", http.StatusNotFound},
		{"GET", "/api/boards?period=year", "", http.StatusBadRequest},
		{"GET", "/api/boards?limit=0", "", http.StatusBadRequest},
	} {
		if code, b := p.do(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.method, c.path, code, b, c.want)
		}
	}
	_, b = p.do("GET", "/api/audit", "")
	var entries []struct{ Action, Detail, Username string }
	json.Unmarshal(b, &entries)
	var saw []string
	for _, e := range entries {
		if e.Action == "manager.plugins" && e.Username == "owner" {
			saw = append(saw, e.Detail)
		}
	}
	slices.Sort(saw)
	if !slices.Equal(saw, []string{"granted hello players.chat", "reloaded hello", "turned on hello"}) {
		t.Fatalf("audited %q", saw)
	}

	// Uploaded as a zip: added off, refused when it's there, replaced when asked.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{{"up/plugin.toml", "api = 0\nname = \"Up\"\nversion = \"1.0.0\""}, {"up/main.lua", "log(1)"}} {
		fw, _ := zw.Create(f[0])
		fw.Write([]byte(f[1]))
	}
	zw.Close()
	if code, b := p.do("POST", "/api/manager/plugins?name=up.zip", buf.String()); code != http.StatusCreated || !strings.Contains(string(b), `"id":"up"`) {
		t.Fatalf("upload: %d %s", code, b)
	}
	code, b = p.do("POST", "/api/manager/plugins?name=up.zip", buf.String())
	var conflict struct {
		Error  string
		Plugin plugins.Status
	}
	if json.Unmarshal(b, &conflict); code != http.StatusConflict || conflict.Error != "Up 1.0.0 is installed already, as up" || conflict.Plugin.ID != "up" {
		t.Fatalf("upload again: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/manager/plugins?name=up.zip&replace=1", buf.String()); code != http.StatusOK {
		t.Fatalf("replace: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/manager/plugins?name=x.zip", "not a zip"); code != http.StatusBadRequest || !strings.Contains(string(b), "it isn't a zip that can be read") {
		t.Fatalf("not a zip: %d %s", code, b)
	}
	if code, b := p.do("POST", "/api/manager/plugins?name=x.zip", strings.Repeat("x", plugins.MaxUpload+1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too big: %d %s", code, b)
	}
	if code, b := p.do("DELETE", "/api/manager/plugins/up", ""); code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", code, b)
	}
	if code, _ := p.do("DELETE", "/api/manager/plugins/up", ""); code != http.StatusNotFound {
		t.Fatalf("remove again: %d", code)
	}
	_, b = p.do("GET", "/api/audit", "")
	json.Unmarshal(b, &entries)
	saw = nil
	for _, e := range entries {
		if e.Action == "manager.plugins" && strings.Contains(e.Detail, "up") {
			saw = append(saw, e.Detail)
		}
	}
	slices.Sort(saw)
	if !slices.Equal(saw, []string{"added up 1.0.0", "removed up", "replaced up 1.0.0"}) {
		t.Fatalf("audited %q", saw)
	}

	off := newPanel(t)
	off.signIn("owner")
	if code, _ := off.do("GET", "/api/manager/plugins", ""); code != http.StatusNotFound {
		t.Fatalf("without plugins: %d", code)
	}
}
