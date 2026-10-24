package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

// The manager keeps the plugins players must have and puts them in each
// server's config as it starts: every server's and its own. It takes back
// out what it put there before that the lists no longer have, and what the
// config required besides stays.
func TestRequiredPlugins(t *testing.T) {
	p, dirs := newServersPanel(t, nil, "", "a", "b")
	cfg := func(id string) string { return filepath.Join(dirs[id], "ReSkateServer.json") }
	writeFile(t, cfg("a"), `{"plugins": {"required": ["Own-Plugin"]}}`)
	writeFile(t, cfg("b"), `{}`)
	start := func(id, wantMsg string) []string {
		t.Helper()
		in, _ := p.api.Reg.Get(id)
		if msg, err := p.api.RequirePlugins(in.Def()); err != nil || !strings.Contains(msg, wantMsg) || (wantMsg == "") != (msg == "") {
			t.Fatalf("start %s: %q %v", id, msg, err)
		}
		f, err := serverconfig.Read(cfg(id))
		if err != nil {
			t.Fatal(err)
		}
		v, _ := f.Values()["plugins.required"].([]string)
		return v
	}

	if code, b := p.do("PATCH", "/api/required-plugins", `{"plugins":["Every-Plugin","Not a package"]}`); code != 400 {
		t.Errorf("a bad name: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/required-plugins", `{"plugins":["Every-Plugin"]}`); code != 200 {
		t.Fatalf("every server: %d %s", code, b)
	}
	if code, b := p.do("PATCH", "/api/instances/a/required-plugins", `{"plugins":["Mine-Plugin-1.0.0","every-plugin"]}`); code != 200 {
		t.Fatalf("server a: %d %s", code, b)
	}
	if got := start("a", ""); !slices.Equal(got, []string{"Own-Plugin", "Mine-Plugin-1.0.0", "every-plugin"}) {
		t.Errorf("a requires %q", got)
	}
	if got := start("b", ""); !slices.Equal(got, []string{"Every-Plugin"}) {
		t.Errorf("b requires %q", got)
	}
	_, b := p.do("GET", "/api/instances/a/mods", "")
	var mods struct {
		Required []string `json:"required"`
		Every    []string `json:"everyRequired"`
		Pending  bool     `json:"requiredPending"`
	}
	if json.Unmarshal(b, &mods); !slices.Equal(mods.Required, []string{"Mine-Plugin-1.0.0", "every-plugin"}) || !slices.Equal(mods.Every, []string{"Every-Plugin"}) || mods.Pending {
		t.Errorf("mods page: %s", b)
	}
	if _, b := p.do("GET", "/api/required-plugins", ""); !strings.Contains(string(b), `"Every-Plugin"`) {
		t.Errorf("every server's: %s", b)
	}

	// Taken off the lists, they leave the configs at the next start; the
	// config's own stays.
	p.do("PATCH", "/api/instances/a/required-plugins", `{"plugins":[]}`)
	sixteen := make([]string, 16)
	for i := range sixteen {
		sixteen[i] = fmt.Sprintf(`"Team-Plugin%d"`, i)
	}
	p.do("PATCH", "/api/required-plugins", `{"plugins":[`+strings.Join(sixteen, ",")+`]}`)
	if got := start("a", "Team-Plugin15"); len(got) != 16 || got[0] != "Own-Plugin" || slices.Contains(got, "Every-Plugin") {
		t.Errorf("a requires %q", got)
	}
	p.do("PATCH", "/api/required-plugins", `{"plugins":[]}`)
	if got := start("a", ""); !slices.Equal(got, []string{"Own-Plugin"}) {
		t.Errorf("a requires %q", got)
	}
	if got := start("b", ""); len(got) != 0 {
		t.Errorf("b requires %q", got)
	}
	// A viewer can't change them.
	p.signIn("viewer")
	if code, _ := p.do("PATCH", "/api/required-plugins", `{"plugins":[]}`); code != 403 {
		t.Errorf("a viewer changed every server's: %d", code)
	}
}
