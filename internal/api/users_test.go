package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
)

// do sends a request as the panel's browser and returns the status and body.
func (p *panel) do(method, path, body string) (int, []byte) {
	p.t.Helper()
	req, _ := http.NewRequest(method, p.url+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-RSM", "1")
	resp, err := p.client.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (p *panel) createUser(name string, grants ...string) int64 {
	p.t.Helper()
	code, b := p.do("POST", "/api/users", fmt.Sprintf(`{"username":%q,"password":%q,"grants":[%s]}`, name, testPassword, strings.Join(grants, ",")))
	if code != http.StatusCreated {
		p.t.Fatalf("create %s: %d %s", name, code, b)
	}
	var u auth.User
	json.Unmarshal(b, &u)
	return u.ID
}

func grant(roleID int64) string { return fmt.Sprintf(`{"roleId":%d,"instance":"*"}`, roleID) }

func TestUserManagersCannotClimb(t *testing.T) {
	p := newPanel(t)
	p.post("/api/auth/login", `{"username":"owner","password":"`+testPassword+`"}`)

	roles := map[string]int64{}
	_, b := p.do("GET", "/api/roles", "")
	var list []auth.Role
	json.Unmarshal(b, &list)
	for _, r := range list {
		roles[r.Name] = r.ID
	}
	code, b := p.do("POST", "/api/roles", `{"name":"User admin","permissions":["panel.users.manage"]}`)
	if code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	var userAdmin auth.Role
	json.Unmarshal(b, &userAdmin)

	code, b = p.do("POST", "/api/roles", `{"name":"Kickers","permissions":["players.kick"]}`)
	var kickers auth.Role
	if json.Unmarshal(b, &kickers); code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}

	mgr := p.createUser("mgr", grant(userAdmin.ID), grant(roles["Moderator"]))
	admin := p.createUser("admin", grant(roles["Administrator"]), grant(kickers.ID))
	p.post("/api/auth/logout", `{}`)
	p.post("/api/auth/login", `{"username":"mgr","password":"`+testPassword+`"}`)

	for _, c := range []struct {
		what, method, path, body string
		want                     int
	}{
		{"give themselves Administrator", "PATCH", fmt.Sprintf("/api/users/%d", mgr),
			`{"grants":[` + grant(userAdmin.ID) + "," + grant(roles["Administrator"]) + `]}`, 403},
		{"create an Administrator", "POST", "/api/users", `{"username":"sock","password":"` + testPassword + `","grants":[` + grant(roles["Administrator"]) + `]}`, 403},
		{"reset an Administrator's password", "PATCH", fmt.Sprintf("/api/users/%d", admin), `{"password":"another long password"}`, 403},
		{"link their Steam to an Administrator", "PATCH", fmt.Sprintf("/api/users/%d", admin), `{"steamId":"76561198000000002"}`, 403},
		{"remove an Administrator", "DELETE", fmt.Sprintf("/api/users/%d", admin), "", 403},
		{"add a permission to their own role", "PATCH", fmt.Sprintf("/api/roles/%d", userAdmin.ID),
			`{"name":"User admin","permissions":["panel.users.manage","settings.edit"]}`, 403},
		{"strip the Administrator role", "PATCH", fmt.Sprintf("/api/roles/%d", roles["Administrator"]), `{"name":"Administrator","permissions":[]}`, 403},
		{"delete the Administrator role", "DELETE", fmt.Sprintf("/api/roles/%d", roles["Administrator"]), "", 403},
		// They hold all of Kickers, but an Administrator holds it too.
		{"empty a role an Administrator holds", "PATCH", fmt.Sprintf("/api/roles/%d", kickers.ID), `{"name":"Kickers","permissions":[]}`, 403},
		{"delete a role an Administrator holds", "DELETE", fmt.Sprintf("/api/roles/%d", kickers.ID), "", 403},
		{"make a role with a permission they lack", "POST", "/api/roles", `{"name":"Updater","permissions":["server.update"]}`, 403},

		{"make a role from what they hold", "POST", "/api/roles", `{"name":"Kicker","permissions":["players.kick"]}`, 200},
		{"create a Moderator", "POST", "/api/users", `{"username":"mod","password":"` + testPassword + `","grants":[` + grant(roles["Moderator"]) + `]}`, 201},
		{"drop their own Moderator grant", "PATCH", fmt.Sprintf("/api/users/%d", mgr), `{"grants":[` + grant(userAdmin.ID) + `]}`, 200},
	} {
		if code, b := p.do(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s: %d %s, want %d", c.what, code, b, c.want)
		}
	}
}

// A new password signs the user out everywhere but where they changed it.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	p := newPanel(t)
	jar, _ := cookiejar.New(nil)
	other := &panel{t: t, url: p.url, client: &http.Client{Jar: jar}, auth: p.auth}
	login := `{"username":"owner","password":"` + testPassword + `"}`
	p.post("/api/auth/login", login)
	other.post("/api/auth/login", login)

	p.post("/api/auth/password", `{"current":"`+testPassword+`","next":"a brand new passphrase"}`)
	if u := p.me(); u == nil {
		t.Fatal("the browser that changed the password was signed out")
	}
	if u := other.me(); u != nil {
		t.Fatal("another browser kept its session after the password changed")
	}
}
