package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/xThrasherrr/ReSkateManager/internal/auth"
	"github.com/xThrasherrr/ReSkateManager/internal/instance"
)

// snapshotLines is how many of the console's lines a newly opened console gets.
const snapshotLines = 300

// socketsPerUser caps the consoles one user may have open, each holding a
// copy of the console and a queue of what happens.
const socketsPerUser = 20

// openSocket counts a console the user opens, unless they have too many.
func (a *API) openSocket(user int64) bool {
	a.sockMu.Lock()
	defer a.sockMu.Unlock()
	if a.sockets[user] >= socketsPerUser {
		return false
	}
	if a.sockets == nil {
		a.sockets = map[int64]int{}
	}
	a.sockets[user]++
	return true
}

func (a *API) closeSocket(user int64) {
	a.sockMu.Lock()
	defer a.sockMu.Unlock()
	if a.sockets[user]--; a.sockets[user] <= 0 {
		delete(a.sockets, user)
	}
}

// roster is a players event as the socket sends it: who is on, to those who
// may see players, and how many, to everyone, as the server list shows.
type roster struct {
	Type     string            `json:"type"`
	Instance string            `json:"instance"`
	Players  []instance.Player `json:"players,omitempty"`
	Count    int               `json:"count"`
}

// ws streams one instance to anyone who can see it: a snapshot first, then
// state changes, console lines and roster changes as they happen. Console
// lines go only to those with console.view, the roster only to those with
// players.view. The socket is read-only for the client; commands go through
// POST /command so each is permission-checked and audited.
func (a *API) ws(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	c := from(r)
	showConsole := c.perms.Can(auth.ConsoleView, in.ID())
	showPlayers := c.perms.Can(auth.PlayersView, in.ID())
	if !a.openSocket(c.user.ID) {
		writeErr(w, http.StatusTooManyRequests, "you have too many consoles open; close a few tabs")
		return
	}
	defer a.closeSocket(c.user.ID)
	// Accept checks the Origin against the Host, so another site cannot open this socket with the user's cookie.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	events, view, console, players := in.Subscribe()
	defer in.Unsubscribe(events)
	// The newest lines only: a long console was slow to open, and the Logs
	// tab has the rest.
	switch {
	case !showConsole:
		console = nil
	case len(console) > snapshotLines:
		console = console[len(console)-snapshotLines:]
	}
	count := len(players)
	if !showPlayers {
		players = nil
	}
	snap := map[string]any{"type": "snapshot", "instance": in.ID(), "state": view, "console": console, "players": players, "count": count}
	if err := write(ctx, conn, snap); err != nil {
		return
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	// Re-check the session now and then, so a revoked user is cut off.
	recheck := time.NewTicker(time.Minute)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				conn.Close(websocket.StatusTryAgainLater, "too slow")
				return
			}
			var msg any = e
			switch e.Type {
			case "console":
				if !showConsole {
					continue
				}
			case "players":
				rs := roster{Type: e.Type, Instance: e.Instance, Count: len(e.Players)}
				if showPlayers {
					rs.Players = e.Players
				}
				msg = rs
			}
			if err := write(ctx, conn, msg); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case <-recheck.C:
			if cur, ok := a.Reg.Get(in.ID()); !ok || cur != in {
				conn.Close(websocket.StatusGoingAway, "server removed")
				return
			}
			u, _ := a.sessionUser(r)
			if u == nil {
				conn.Close(websocket.StatusPolicyViolation, "signed out")
				return
			}
			p, err := a.Auth.Perms(ctx, u)
			if err != nil {
				continue // the database, not the user: try again next time
			}
			// Access changed: the client connects again, to a snapshot that fits.
			if !p.Sees(in.ID()) || p.Can(auth.ConsoleView, in.ID()) != showConsole || p.Can(auth.PlayersView, in.ID()) != showPlayers {
				conn.Close(websocket.StatusPolicyViolation, "access changed")
				return
			}
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, data)
}
