// Package logparse turns ReSkateServer stdout into typed entries.
//
// The server prints every log entry as "[HH:MM:SS] text". A multi-line entry
// (a command reply such as `players`) is one printf, so only its first line is
// stamped; the lines after it belong to the same entry. The formats matched
// here come from Server/server_host.cpp, server_votes.cpp, server_party.cpp,
// server_activity.cpp and main.cpp in the ReSkate repo.
package logparse

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind is what a line of the server's output is: a join, chat, a reply...
type Kind string

const (
	KindRaw       Kind = "raw"       // anything not recognised; command replies land here
	KindInput     Kind = "input"     // a command the manager sent (never printed by the server)
	KindManager   Kind = "manager"   // a line the manager itself adds to the console
	KindJoin      Kind = "join"      // <name> joined (<id>[, admin]), N/M players[, loaded in X s]
	KindLeave     Kind = "leave"     // <name> left (<reason>) [<how it ended>]
	KindChat      Kind = "chat"      // [chat] <name>: <text>
	KindCommand   Kind = "command"   // [command] <name>: /<text>   (a player's chat command)
	KindAdmin     Kind = "admin"     // [admin] <name>: <text>      (an in-game admin's server command)
	KindPartyChat Kind = "partychat" // [party chat] <name>: <text>
	KindTagged    Kind = "tagged"    // [vote], [party], [dm], [throwdown], [objects], [map], [anticheat], [join], [network], [steam], [direct], [afk]
	KindReady     Kind = "ready"     // <name> is up on <map> for N players.
	KindSteam     Kind = "steam"     // Steam ID <id>[ (<how it got it>)], public IP <ip>.
	KindJoinCode  Kind = "joincode"  // Join code: <code>[ (password required)]
	KindLoaded    Kind = "loaded"    // Everyone has loaded <map>.
	KindShutdown  Kind = "shutdown"  // Shutting down. / Restarting for an update.
	KindStartup   Kind = "startup"   // other lines the server prints on its own (startup, updates, errors)
)

// Entry is one log entry, possibly spanning several lines.
type Entry struct {
	Seq    uint64            `json:"seq"`
	Stamp  string            `json:"stamp,omitempty"` // HH:MM:SS as the server printed it
	At     int64             `json:"at"`              // unix ms when the manager read it
	Text   string            `json:"text"`            // continuation lines joined with \n
	Kind   Kind              `json:"kind"`
	Tag    string            `json:"tag,omitempty"` // the [tag] for tagged kinds
	Name   string            `json:"name,omitempty"`
	ID     string            `json:"id,omitempty"` // SteamID64 when the line carries one
	Fields map[string]string `json:"fields,omitempty"`
}

var (
	stampRe    = regexp.MustCompile(`^\[(\d{2}:\d{2}:\d{2})\] ?(.*)$`)
	tagRe      = regexp.MustCompile(`^\[(chat|command|admin|party chat|party|dm|vote|throwdown|objects|map|rotation|anticheat|join|network|steam|direct|afk)\] (.*)$`)
	joinRe     = regexp.MustCompile(`^(.+) joined \((\d+)(, admin)?\), (\d+)/(\d+) players(?:, loaded in (\d+) s)?$`)
	leaveRe    = regexp.MustCompile(`^(.+) left \((.*)\)(?: \[.*\])?$`) // then how the connection ended, since 1.1.5
	readyRe    = regexp.MustCompile(`^(.+) is up on (.+) for (\d+) players\.$`)
	steamRe    = regexp.MustCompile(`^Steam ID (\d+)(?: \([^)]*\))?, public IP (.*)\.$`) // the (...) since 1.1.4
	joinCodeRe = regexp.MustCompile(`^Join code: (\S+)( \(password required\))?$`)
	loadedRe   = regexp.MustCompile(`^Everyone has loaded (.+)\.$`)
	// The server names the player by SteamID64 in its reply, whatever the
	// command was given; a player's name can't pass for one.
	adminRe = regexp.MustCompile(`^(7656119\d{10}) is (an admin|no longer an admin)\.$`)
)

// SplitStamp separates "[HH:MM:SS] text". ok is false for a continuation line.
func SplitStamp(line string) (stamp, text string, ok bool) {
	m := stampRe.FindStringSubmatch(line)
	if m == nil {
		return "", line, false
	}
	return m[1], m[2], true
}

// Lines Server/main.cpp prints by itself, at startup or between commands
// (update checks, the name filter, errors); never command replies.
var startupPrefixes = []string{"Signing in to Steam", "Wrote a default ", "Added new settings to ", "Mods: ", "World layers: ",
	"world-layers.json is unreadable", "Checking for updates", "The server is up to date", "Update check skipped",
	"No admins yet: ", "Config problem: ", "Cannot read ", "Cannot load ", "Steam sign-in timed out", "Steam networking failed",
	"Could not open the server", "The server name \"", "The server name is allowed again", "Server update ",
	"Update check failed: ", "Restarting to install server update ", "Nobody is on; restarting", "Installing server update ",
	"Could not start the updated server", "This is a local build", "Server error: ", "Could not save the config: ",
	"Global bans", "The global ban list could not be read", "No steam_token: ", "The server browser now shows only servers with a steam_token",
	"The server browser shows servers without a steam_token again", "Connection: ", "Steam networking debug output is on",
	"steam_debug: "}

// Classify fills Kind, Tag, Name, ID and Fields from e.Text.
func Classify(e *Entry) {
	first, _, _ := strings.Cut(e.Text, "\n")
	e.Kind = KindRaw
	if m := tagRe.FindStringSubmatch(first); m != nil {
		e.Tag = m[1]
		body := m[2]
		switch e.Tag {
		case "chat", "command", "admin", "party chat":
			e.Kind = map[string]Kind{"chat": KindChat, "command": KindCommand, "admin": KindAdmin, "party chat": KindPartyChat}[e.Tag]
			if name, msg, ok := strings.Cut(body, ": "); ok {
				e.Name = name
				e.Fields = map[string]string{"message": msg}
			}
		default:
			e.Kind = KindTagged
		}
		return
	}
	if m := joinRe.FindStringSubmatch(first); m != nil {
		e.Kind, e.Name, e.ID = KindJoin, m[1], m[2]
		e.Fields = map[string]string{"players": m[4], "max": m[5]}
		if m[3] != "" {
			e.Fields["admin"] = "1"
		}
		if m[6] != "" {
			e.Fields["load_seconds"] = m[6]
		}
		return
	}
	if m := readyRe.FindStringSubmatch(first); m != nil {
		e.Kind, e.Name = KindReady, m[1]
		e.Fields = map[string]string{"map": m[2], "max": m[3]}
		return
	}
	if m := steamRe.FindStringSubmatch(first); m != nil {
		e.Kind, e.ID = KindSteam, m[1]
		e.Fields = map[string]string{"public_ip": m[2]}
		return
	}
	if m := joinCodeRe.FindStringSubmatch(first); m != nil {
		e.Kind = KindJoinCode
		e.Fields = map[string]string{"code": m[1]}
		if m[2] != "" {
			e.Fields["password"] = "1"
		}
		return
	}
	if m := loadedRe.FindStringSubmatch(first); m != nil {
		e.Kind = KindLoaded
		e.Fields = map[string]string{"map": m[1]}
		return
	}
	if first == "Shutting down." || first == "Restarting for an update." {
		e.Kind = KindShutdown
		return
	}
	for _, p := range startupPrefixes {
		if strings.HasPrefix(first, p) {
			e.Kind = KindStartup
			return
		}
	}
	if strings.HasSuffix(first, " admin(s). Type help for commands.") {
		e.Kind = KindStartup
		return
	}
	// Last: "left (...)" is loose enough to match a reply, so it goes after the others.
	if m := leaveRe.FindStringSubmatch(first); m != nil && !strings.Contains(e.Text, "\n") {
		e.Kind, e.Name = KindLeave, m[1]
		e.Fields = map[string]string{"reason": m[2]}
	}
}

// IsEvent reports whether an entry is something the server logs on its own,
// as opposed to a reply to a console command.
func (e *Entry) IsEvent() bool {
	switch e.Kind {
	case KindRaw:
		return false
	case KindChat:
		// `say` replies "[chat] Server: <text>".
		return e.Name != "Server"
	}
	return true
}

// ParseAdminChange reads the server's reply to `admin add|remove`: who it
// names (a SteamID64, or the player name it was given) and whether they are
// an admin now.
func ParseAdminChange(text string) (who string, admin, ok bool) {
	m := adminRe.FindStringSubmatch(text)
	if m == nil {
		return "", false, false
	}
	return m[1], m[2] == "an admin", true
}

// IsCommandError reports whether a reply is the server refusing a command
// rather than answering it.
func IsCommandError(text string) bool {
	return strings.HasPrefix(text, "Unknown command ") || strings.HasPrefix(text, "Command failed: ")
}

// RosterLine is one player in the reply to `players`.
type RosterLine struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
}

var playersHeadRe = regexp.MustCompile(`^(\d+) players$`)

// ParsePlayers reads the reply to `players`:
//
//	N players
//	  <SteamID64>  <name>[  (admin)]
func ParsePlayers(text string) ([]RosterLine, bool) {
	lines := strings.Split(text, "\n")
	m := playersHeadRe.FindStringSubmatch(lines[0])
	if m == nil {
		return nil, false
	}
	want, _ := strconv.Atoi(m[1])
	out := make([]RosterLine, 0, want)
	for _, l := range lines[1:] {
		l = strings.TrimPrefix(l, "  ")
		id, rest, ok := strings.Cut(l, "  ")
		if !ok {
			continue
		}
		r := RosterLine{ID: id, Name: rest}
		if n, found := strings.CutSuffix(rest, "  (admin)"); found {
			r.Name, r.Admin = n, true
		}
		out = append(out, r)
	}
	return out, len(out) == want
}

var mapsHeadRe = regexp.MustCompile(`^\d+ maps\b`)

// ParseMaps reads the reply to "maps": a count line, then one map per line,
// the current one marked "(now)" and those in the map pool "(pool)".
func ParseMaps(text string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	if !mapsHeadRe.MatchString(lines[0]) {
		return nil, false
	}
	out := []string{}
	for _, l := range lines[1:] {
		name := strings.TrimSpace(l)
		for _, mark := range []string{"(pool)", "(now)"} { // last to first, as the server adds them
			name = strings.TrimSpace(strings.TrimSuffix(name, mark))
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out, true
}
