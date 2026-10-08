package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestSocketFollowsPermissions checks what the live feed sends each user:
// console lines only with console.view, the roster only with players.view,
// and the server's state and head count to anyone who can see it.
func TestSocketFollowsPermissions(t *testing.T) {
	p, _ := newServersPanel(t, nil, "", "a")
	code, b := p.do("POST", "/api/roles", `{"name":"Settings","permissions":["settings.view"]}`)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create role: %d %s", code, b)
	}
	p.createUser("settings", grant(p.roleID("Settings")))
	in, _ := p.api.Reg.Get("a")
	in.Note("before anyone looked")

	dial := func(user string) *websocket.Conn {
		t.Helper()
		p.signIn(user)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.url, "http")+"/api/instances/a/ws", &websocket.DialOptions{HTTPClient: p.client})
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		t.Cleanup(func() { conn.CloseNow() })
		return conn
	}
	next := func(conn *websocket.Conn) map[string]json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatal(err)
		}
		return msg
	}

	viewer := next(dial("viewer"))
	if !strings.Contains(string(viewer["console"]), "before anyone looked") || string(viewer["count"]) != "0" {
		t.Errorf("viewer's snapshot: %s", viewer)
	}

	conn := dial("settings")
	snap := next(conn)
	if string(snap["console"]) != "null" || string(snap["players"]) != "null" || string(snap["count"]) != "0" || len(snap["state"]) < 10 {
		t.Errorf("settings-only snapshot: %s", snap)
	}
	// The note is left out; the state change after it comes through.
	in.Note("not for them")
	if err := in.BeginUpdate(); err != nil {
		t.Fatal(err)
	}
	defer in.EndUpdate()
	if msg := next(conn); string(msg["type"]) != `"state"` {
		t.Errorf("settings-only user got %s", msg)
	}
}
