package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// A user granted announcements on one server manages that server's, but not
// another server's nor those for every server.
func TestAnnouncementScopes(t *testing.T) {
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
	log := slog.New(slog.DiscardHandler)
	reg := instance.NewRegistry(log)
	for _, id := range []string{"a", "b"} {
		reg.Add(instance.Def{ID: id, Name: id, Dir: t.TempDir()})
	}
	srv := httptest.NewServer((&API{Store: st, Auth: svc, Reg: reg, Log: log, Static: fstest.MapFS{}}).Handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	p := &panel{t: t, url: srv.URL, client: &http.Client{Jar: jar}, auth: svc}

	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)
	_, b := p.do("POST", "/api/roles", `{"name":"Announcer","permissions":["announcements.manage"]}`)
	var role auth.Role
	json.Unmarshal(b, &role)
	p.createUser("ann", fmt.Sprintf(`{"roleId":%d,"instance":"a"}`, role.ID))
	add := func(inst, body string) store.Announcement {
		code, b := p.do("POST", "/api/instances/"+inst+"/announcements", body)
		if code != 200 {
			t.Fatalf("owner add on %s: %d %s", inst, code, b)
		}
		var x store.Announcement
		json.Unmarshal(b, &x)
		return x
	}
	every := add("a", `{"allServers":true,"message":"discord","interval":3600,"enabled":true}`)
	other := add("b", `{"message":"b only","interval":600,"enabled":true}`)
	p.post("/api/auth/logout", `{}`)
	p.post("/api/auth/login", `{"username":"ann","password":"`+testPassword+`"}`)

	for _, c := range []struct {
		what, method, path, body string
		want                     int
	}{
		{"add on their server", "POST", "/api/instances/a/announcements", `{"message":"hi","interval":600,"enabled":true}`, 200},
		{"add for every server", "POST", "/api/instances/a/announcements", `{"allServers":true,"message":"hi","interval":600,"enabled":true}`, 403},
		{"edit the every-server one", "PATCH", fmt.Sprintf("/api/instances/a/announcements/%d", every.ID), `{"allServers":true,"message":"x","interval":600,"enabled":false}`, 403},
		{"move the every-server one to their server", "PATCH", fmt.Sprintf("/api/instances/a/announcements/%d", every.ID), `{"message":"x","interval":600,"enabled":true}`, 403},
		{"remove the every-server one", "DELETE", fmt.Sprintf("/api/instances/a/announcements/%d", every.ID), "", 403},
		{"reach another server's through theirs", "DELETE", fmt.Sprintf("/api/instances/a/announcements/%d", other.ID), "", 404},
		{"list another server's", "GET", "/api/instances/b/announcements", "", 404},
		{"repeat faster than a minute", "POST", "/api/instances/a/announcements", `{"message":"hi","interval":30,"enabled":true}`, 400},
		{"send an over-long message", "POST", "/api/instances/a/announcements", `{"message":"` + strings.Repeat("x", 201) + `","interval":600,"enabled":true}`, 400},
	} {
		if code, b := p.do(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.what, code, b, c.want)
		}
	}

	_, b = p.do("GET", "/api/instances/a/announcements", "")
	var got struct {
		Announcements []store.Announcement `json:"announcements"`
		AllServers    bool                 `json:"allServers"`
	}
	json.Unmarshal(b, &got)
	if len(got.Announcements) != 2 || got.AllServers {
		t.Fatalf("list on a = %s", b)
	}
}
