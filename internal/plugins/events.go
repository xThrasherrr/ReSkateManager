package plugins

import (
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/logparse"
)

// Events are what on() takes: the server's notices, by these names.
var Events = []string{
	"player.joined", "player.left", "chat", "map", "state",
	"throwdown.placed", "throwdown.removed", "throwdown.joined", "throwdown.left", "throwdown.started",
	"throwdown.quit", "throwdown.lead", "throwdown.scored", "throwdown.landed", "throwdown.missed",
	"throwdown.finished",
}

func knownEvent(name string) bool { return slices.Contains(Events, name) }

// event is a notice as a plugin gets it: its name, and the table its
// handlers are called with. Times are unix seconds; a player's id is their
// SteamID64, missing when a chat line's name could be more than one player.
func event(server string, n instance.Notice) (string, map[string]any) {
	e := map[string]any{"server": server}
	switch n.Kind {
	case instance.NoticeJoined:
		putPlayer(e, n.Player)
		e["joined_at"] = n.Player.JoinedAt / 1000
		return "player.joined", e
	case instance.NoticeLeft:
		putPlayer(e, n.Player)
		e["reason"] = n.Leave.Reason
		e["joined_at"] = n.Player.JoinedAt / 1000
		e["left_at"] = n.Leave.At / 1000
		e["seconds"] = max(0, (n.Leave.At-n.Player.JoinedAt)/1000)
		return "player.left", e
	case instance.NoticeChat:
		e["name"] = n.Player.Name
		if n.Player.ID != "" {
			e["id"] = n.Player.ID
		}
		e["text"] = n.Text
		return "chat", e
	case instance.NoticeMap:
		e["map"] = n.Map
		return "map", e
	case instance.NoticeState:
		e["state"] = string(n.State)
		e["was"] = string(n.Was)
		if n.Map != "" {
			e["map"] = n.Map
		}
		return "state", e
	case instance.NoticeThrowdown:
		throwdown(e, n.Throwdown)
		return "throwdown." + n.Throwdown.Kind, e
	case instance.NoticeCommand:
		e["name"] = n.Player.Name
		if n.Player.ID != "" {
			e["id"] = n.Player.ID
		}
		args := []any{}
		for _, a := range strings.Fields(n.Text) {
			args = append(args, a)
		}
		e["command"], e["args"], e["text"] = n.Command, args, n.Text
		return "command:" + n.Command, e
	}
	return "", nil
}

func putPlayer(e map[string]any, p instance.Player) {
	e["id"], e["name"] = p.ID, p.Name
	if p.Role != "" {
		e["role"] = p.Role
	}
	e["admin"] = p.Admin
}

func named(n logparse.Named) map[string]any {
	m := map[string]any{"name": n.Name}
	if n.ID != "" {
		m["id"] = n.ID
	}
	return m
}

// throwdown fills a throwdown line's fields: mode is "jam", "spot_battle",
// "skate" or "" for one the server didn't name.
func throwdown(e map[string]any, t logparse.Throwdown) {
	e["kind"], e["mode"] = t.Kind, t.Mode
	e["leader"] = named(t.Leader)
	if t.Player.Name != "" {
		e["player"] = named(t.Player)
	}
	switch t.Kind {
	case logparse.ThrowdownLead:
		e["points"] = t.Points
	case logparse.ThrowdownScored:
		e["points"], e["round"] = t.Points, t.Round
		if t.Best != 0 {
			e["best"] = t.Best
		}
	case logparse.ThrowdownLanded, logparse.ThrowdownMissed:
		e["turn"] = t.Round
	case logparse.ThrowdownJoined:
		e["queue"] = t.Queue
	case logparse.ThrowdownPlaced:
		if t.Near != nil {
			e["near"] = map[string]any{"x": t.Near[0], "y": t.Near[1], "z": t.Near[2]}
		}
	case logparse.ThrowdownStarted:
		players := make([]any, len(t.Players))
		for i, p := range t.Players {
			players[i] = named(p)
		}
		e["players"] = players
	case logparse.ThrowdownFinished:
		results := make([]any, len(t.Results))
		for i, r := range t.Results {
			m := named(r.Named)
			if t.Mode == logparse.ModeSkate {
				m["landed"], m["missed"] = r.Landed, r.Missed
			} else {
				m["place"], m["points"] = r.Place, r.Points
			}
			m["quit"] = r.Quit
			results[i] = m
		}
		e["results"] = results
	}
}
