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
	KindJoin      Kind = "join"      // <name> joined (<id>[, <role>]), N/M players[, loaded in X s]
	KindLeave     Kind = "leave"     // <name> left (<reason>) [<how it ended>]
	KindChat      Kind = "chat"      // [chat] <name>: <text>
	KindCommand   Kind = "command"   // [command] <name>: /<text>   (a player's chat command; [command] <name> /<text> as one of the owner's runs)
	KindAdmin     Kind = "admin"     // [admin] <name>: <text>      (an in-game admin's server command)
	KindPartyChat Kind = "partychat" // [party chat] <name>: <text>
	KindTagged    Kind = "tagged"    // [vote], [poll], [announcement], [words], [party], [dm], [throwdown], [objects], [map], [anticheat], [join], [network], [steam], [direct], [afk], [parks], [teleport], [Plugins], [hide], [world], [card by plugin x], [poll by plugin x]
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

// stampTime is the time in a line's stamp: HH:MM:SS, but ReSkate 2.0.6's
// Windows server writes it as Windows shows the time, which follows the
// user's settings: "1:28:15 PM", "13.28.15"...
const stampTime = `[^\[\]]{0,24}?\d{1,2}[:.]\d{2}(?:[:.]\d{2})?[^\[\]]{0,24}?`

var (
	// A line's stamp is "[HH:MM:SS] ".
	stampRe = regexp.MustCompile(`^\[(` + stampTime + `)\] ?(.*)$`)
	clockRe = regexp.MustCompile(`(\d{1,2})[:.](\d{2})(?:[:.](\d{2}))?`)
	pmRe    = regexp.MustCompile(`(?i)\bp\.? ?m\b`)
	amRe    = regexp.MustCompile(`(?i)\ba\.? ?m\b`)
	// Since 2.0.3 a tag can name the one player a line was for: "[announcement -> Bob] ...".
	// [Plugins] is every line the server's plugins log, whenever they run
	// (Server/server_plugins.cpp, ReSkate 2.1.2); [hide] and [world] are what
	// they do to players and the world.
	tagRe = regexp.MustCompile(`^\[(chat|command|admin|party chat|party|dm|vote|poll|announcement|words|throwdown|objects|map|rotation|anticheat|join|network|steam|direct|afk|parks|teleport|lock|Plugins|hide|world)(?: -> (.+?))?\] (.*)$`)
	// A plugin's card or poll (2.1.2) names the plugin, a poll its number,
	// and whom it went to: "[poll by plugin <id> #<n> -> <who>] <text>".
	pluginTagRe = regexp.MustCompile(`^\[(card|poll) by plugin (.+?)(?: #\d+)?(?: -> (.+?))?\] (.*)$`)
	// One of the owner's chat commands running (2.0.3): "<name> /<command>[ <text>]".
	customCommandRe = regexp.MustCompile(`^(.+?) /([a-z0-9_-]{1,16})(?: .*)?$`)
	// ", admin" before 2.0.6; then the player's role, which is everyone's
	// default one ("player") when they were given none.
	joinRe     = regexp.MustCompile(`^(.+) joined \((\d+)(?:, ([a-z0-9_-]{1,24}))?\), (\d+)/(\d+) players(?:, loaded in (\d+) s)?$`)
	leaveRe    = regexp.MustCompile(`^(.+) left \((.*)\)(?: \[.*\])?$`) // then how the connection ended, since 1.1.5
	readyRe    = regexp.MustCompile(`^(.+) is up on (.+) for (\d+) players\.$`)
	steamRe    = regexp.MustCompile(`^Steam ID (\d+)(?: \([^)]*\))?, public IP (.*)\.$`) // the (...) since 1.1.4
	joinCodeRe = regexp.MustCompile(`^Join code: (\S+)( \(password required\))?$`)
	loadedRe   = regexp.MustCompile(`^Everyone has loaded (.+)\.$`)
	// The server names the player by SteamID64 in its reply, whatever the
	// command was given; a player's name can't pass for one.
	adminRe = regexp.MustCompile(`^(7656119\d{10}) is (an admin|no longer an admin)\.$`)
	// 2.0.6's role command names the player as they're called on the server,
	// or by SteamID64 when they're not on: "<who> is now <role>.", or with
	// it taken away "<who> is <default role> again." or "<who> no longer has
	// a role."
	roleRe     = regexp.MustCompile(`^(.+) is now ([a-z0-9_-]{1,24})\.$`)
	roleBackRe = regexp.MustCompile(`^(.+) is ([a-z0-9_-]{1,24}) again\.$`)
	roleGoneRe = regexp.MustCompile(`^(.+) no longer has a role\.$`)
	// A player in the reply to players: "<name>  (<role>)" from 2.0.6.
	rosterRoleRe = regexp.MustCompile(`^(.*)  \(([a-z0-9_-]{1,24})\)$`)
)

// SplitStamp separates "[HH:MM:SS] text", the stamp as it was written. ok is
// false for a continuation line.
func SplitStamp(line string) (stamp, text string, ok bool) {
	m := stampRe.FindStringSubmatch(line)
	if m == nil {
		return "", line, false
	}
	return m[1], m[2], true
}

// Clock reads the time of day in a stamp, written as HH:MM:SS or the way
// Windows shows the time (3:04:05 PM). An AM or PM in another language isn't
// known, so such an afternoon reads as the morning.
func Clock(stamp string) (h, m, s int, ok bool) {
	c := clockRe.FindStringSubmatch(stamp)
	if c == nil {
		return 0, 0, 0, false
	}
	h, _ = strconv.Atoi(c[1])
	m, _ = strconv.Atoi(c[2])
	s, _ = strconv.Atoi(c[3]) // 0 when the format has no seconds
	switch {
	case h < 12 && pmRe.MatchString(stamp):
		h += 12
	case h == 12 && amRe.MatchString(stamp):
		h = 0
	}
	if h > 23 || m > 59 || s > 59 {
		return 0, 0, 0, false
	}
	return h, m, s, true
}

// Lines Server/main.cpp prints by itself, at startup or between commands
// (update checks, the name filter, errors); never command replies.
var startupPrefixes = []string{"Signing in to Steam", "Wrote a default ", "Added new settings to ", "Mods: ", "World layers: ",
	"world-layers.json is unreadable", "Checking for updates", "The server is up to date", "Update check skipped",
	"No admins yet: ", "Config problem: ", "Cannot read ", "Cannot load ", "Steam sign-in timed out", "Steam networking failed",
	"Could not open the server", "The server name \"", "The server name is allowed again", "Server update ",
	"Update check failed: ", "Restarting to install server update ", "Nobody is on; restarting", "Installing server update ",
	"Could not start the updated server", "This is a local build", "Server error: ", "Could not save the config: ",
	"Global bans", "The global ban list could not be read", "The ReSkate team's lists (global bans and chat words) could not be read",
	"Word lists: ", "No steam_token: ", "The server browser now shows only servers with a steam_token",
	"The server browser shows servers without a steam_token again", "Connection: ", "Steam networking debug output is on",
	"steam_debug: ", "Threads: ", ChatWords, "The role ", MapVersions,
	"Required plugin"} // 2.1.2 checks plugins.required on Thunderstore as it starts

// ChatWords starts what words answers, which 2.0.6 prints at startup too,
// straight after it's up.
const ChatWords = "Chat words: "

// chatKinds are the kinds of the tags a player's own line has, by tag.
var chatKinds = map[string]Kind{"chat": KindChat, "command": KindCommand, "admin": KindAdmin, "party chat": KindPartyChat}

// Classify fills Kind, Tag, Name, ID and Fields from e.Text.
func Classify(e *Entry) {
	first, _, _ := strings.Cut(e.Text, "\n")
	e.Kind = KindRaw
	if m := tagRe.FindStringSubmatch(first); m != nil {
		e.Tag = m[1]
		body := m[3]
		switch e.Tag {
		case "chat", "command", "admin", "party chat":
			e.Kind = chatKinds[e.Tag]
			if name, msg, ok := strings.Cut(body, ": "); ok {
				e.Name = name
				e.Fields = map[string]string{"message": msg}
			}
			// Typed, a command is logged "<name>: /<text>"; one of the owner's
			// is logged again as it runs, without the colon.
			if e.Tag == "command" && !strings.HasPrefix(e.Fields["message"], "/") {
				e.Name, e.Fields = "", nil
				if c := customCommandRe.FindStringSubmatch(body); c != nil {
					e.Name = c[1]
					e.Fields = map[string]string{"message": body[len(c[1])+1:], "custom": c[2]}
				}
			}
		default:
			e.Kind, e.Name = KindTagged, m[2]
		}
		return
	}
	if m := pluginTagRe.FindStringSubmatch(first); m != nil {
		e.Kind, e.Tag, e.Name = KindTagged, m[1], m[3]
		e.Fields = map[string]string{"plugin": m[2]}
		return
	}
	if m := joinRe.FindStringSubmatch(first); m != nil {
		e.Kind, e.Name, e.ID = KindJoin, m[1], m[2]
		e.Fields = map[string]string{"players": m[4], "max": m[5]}
		if m[3] != "" {
			e.Fields["role"] = m[3]
		}
		if m[3] == "admin" {
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
	// "N admin(s). Type help...", and from 2.0.6 "N admin(s), M player(s) with a role. Type help..."
	if strings.Contains(first, " admin(s)") && strings.HasSuffix(first, ". Type help for commands.") {
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

// ParseRoleChange reads the server's reply to the command it answers: to
// `admin add|remove`, who it names (a SteamID64) and whether they're an admin
// now, as role "admin" or none; to 2.0.6's `role`, who (a SteamID64, or a
// player's name when they're on) and the role they have now, "" for none.
// Only those commands count: their replies would read the same as others'.
func ParseRoleChange(command, reply string) (who, role string, ok bool) {
	switch verb, _, _ := strings.Cut(strings.ToLower(command), " "); verb {
	case "admin":
		if m := adminRe.FindStringSubmatch(reply); m != nil {
			if m[2] == "an admin" {
				return m[1], "admin", true
			}
			return m[1], "", true
		}
	case "role":
		if m := roleRe.FindStringSubmatch(reply); m != nil {
			return m[1], m[2], true
		}
		if m := roleBackRe.FindStringSubmatch(reply); m != nil {
			return m[1], m[2], true
		}
		if m := roleGoneRe.FindStringSubmatch(reply); m != nil {
			return m[1], "", true
		}
	}
	return "", "", false
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
	Role  string `json:"role,omitempty"` // "admin" for an admin before 2.0.6
	Admin bool   `json:"admin"`
}

// PlayersHeadRe is the first line of the reply to `players`.
var PlayersHeadRe = regexp.MustCompile(`^(\d+) players$`)

// ParsePlayers reads the reply to `players`:
//
//	N players
//	  <SteamID64>  <name>[  (admin)]
//
// From 2.0.6 each player's role is in the brackets, the default role
// ("player") for those given none.
func ParsePlayers(text string) ([]RosterLine, bool) {
	lines := strings.Split(text, "\n")
	m := PlayersHeadRe.FindStringSubmatch(lines[0])
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
		if m := rosterRoleRe.FindStringSubmatch(rest); m != nil {
			r.Name, r.Role, r.Admin = m[1], m[2], m[2] == "admin"
		}
		out = append(out, r)
	}
	return out, len(out) == want
}

// MapVersions starts what 2.0.7's maps.latest_versions finds on Thunderstore,
// which the server prints on its own: "Map versions: <folder> is now at
// <version> ...", or why it left a mod as it was. Once one is at a newer
// version, the server reads Mods again and prints MapsReread.
const MapVersions = "Map versions: "

// MapsReread starts what `maps reload` answers (ReSkate 2.0.7), which the
// server also prints by itself after maps.latest_versions moved a mod on.
const MapsReread = "Maps read again from Mods: "

// MapsHeadRe is the first line of the reply to `maps`.
var MapsHeadRe = regexp.MustCompile(`^\d+ maps\b`)

// ParseMaps reads the reply to "maps": a count line, then one map per line,
// the current one marked "(now)" and those in the map pool "(pool)".
func ParseMaps(text string) ([]string, bool) {
	lines := strings.Split(text, "\n")
	if !MapsHeadRe.MatchString(lines[0]) {
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

// LocksHeadRe is the first line of the reply to `locks` (ReSkate 2.1.0).
var LocksHeadRe = regexp.MustCompile(`^(\d+) players? ha(?:s|ve) locked tools\b`)

// Lock is a player whose tools are locked, as `locks` lists them.
type Lock struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Here      bool     `json:"here"`
	Tools     []string `json:"tools"`     // an admin's lock, kept when they leave
	Throwdown []string `json:"throwdown"` // their throwdown's, while it runs
}

// The tools as lock_tools_text lists them, ollies and objects from 2.1.2;
// only these, so a name with ": " in it still ends where they start.
const lockTool = `(?:noclip|nobail|boosts|tuning|ollies|objects)`
const lockToolsRe = `(` + lockTool + `(?:, ` + lockTool + `)*)`

var lockLineRe = regexp.MustCompile(`^(\d+)  (.*?)( \(not here\))?: ` + lockToolsRe + `? ?(?:\(throwdown: ` + lockToolsRe + `\))?$`)

// ParseLocks reads the reply to `locks`:
//
//	N players have locked tools[ (throwdown-locks: <tools>)]
//	  <SteamID64>  <name>[ (not here)]: [<tools>][ (throwdown: <tools>)]
func ParseLocks(text string) ([]Lock, bool) {
	lines := strings.Split(text, "\n")
	m := LocksHeadRe.FindStringSubmatch(lines[0])
	if m == nil {
		return nil, false
	}
	want, _ := strconv.Atoi(m[1])
	tools := func(s string) []string {
		if s == "" {
			return []string{}
		}
		return strings.Split(s, ", ")
	}
	out := make([]Lock, 0, want)
	for _, l := range lines[1:] {
		if m := lockLineRe.FindStringSubmatch(strings.TrimPrefix(l, "  ")); m != nil {
			out = append(out, Lock{ID: m[1], Name: m[2], Here: m[3] == "", Tools: tools(m[4]), Throwdown: tools(m[5])})
		}
	}
	return out, len(out) == want
}

// PluginsHeadRe is the first line of the reply to `plugin list` (ReSkate
// 2.1.2): a plugin, or that there are none.
var PluginsHeadRe = regexp.MustCompile(`^(?:No server plugins\.|[^:\n]+: (?:running|stopped|switched off|not loaded)\b)`)

// ServerPlugin is one of ReSkate's own plugins the server runs, as `plugin
// list` lists it, by the folder it found it in.
type ServerPlugin struct {
	ID     string  `json:"id"`
	State  string  `json:"state"`            // running, stopped, switched off or not loaded
	Reason string  `json:"reason,omitempty"` // why it isn't running
	MB     float64 `json:"mb,omitempty"`     // the memory a running one uses
	TickMS float64 `json:"tickMs,omitempty"` // and how long its tick takes
}

var pluginLineRe = regexp.MustCompile(`^(.+?): (running|stopped|switched off|not loaded)(?:, ([\d.]+) MB, ([\d.]+) ms a tick| \((.*))?$`)

// ParsePlugins reads the reply to `plugin list`:
//
//	<id>: running, <n> MB, <n> ms a tick
//	<id>: stopped|switched off|not loaded[ (<why>)]
func ParsePlugins(text string) ([]ServerPlugin, bool) {
	out := []ServerPlugin{}
	if strings.HasPrefix(text, "No server plugins.") {
		return out, true
	}
	for l := range strings.SplitSeq(text, "\n") {
		m := pluginLineRe.FindStringSubmatch(l)
		if m == nil {
			// A why can run over lines, as a Luau error's does.
			if len(out) == 0 {
				return nil, false
			}
			out[len(out)-1].Reason += "\n" + l
			continue
		}
		mb, _ := strconv.ParseFloat(m[3], 64)
		tick, _ := strconv.ParseFloat(m[4], 64)
		out = append(out, ServerPlugin{ID: m[1], State: m[2], Reason: m[5], MB: mb, TickMS: tick})
	}
	for i := range out {
		out[i].Reason = strings.TrimSuffix(out[i].Reason, ")")
	}
	return out, true
}

// ParksHeadRe is the first line of the reply to `park-mod list` (ReSkate
// 2.0.6), or the one it gives when no park mod is set up.
var ParksHeadRe = regexp.MustCompile(`^(?:Park mods \((\d+) of \d+ spawned, \d+ of \d+ objects\):|No park mods are set up\b)`)

// SpawnedPark is a park mod that's up, as `park-mod list` lists it.
type SpawnedPark struct {
	Name    string `json:"name"`
	Objects int    `json:"objects"`
	Minutes int    `json:"minutes,omitempty"` // left before it goes; 0 until removed
}

// A folder can't hold a quote, and the map ends where the spawned part starts.
var parkLineRe = regexp.MustCompile(`^([a-z0-9_-]+)  "[^"]*"(?: \([a-z0-9_]+\))? on .*?  spawned, (\d+) objects, (?:(\d+) min left|until removed)$`)

// ParseParks reads the reply to `park-mod list`, keeping the spawned ones:
//
//	Park mods (N of M spawned, X of Y objects):
//	  <name>  "<folder>"[ (<key>)] on <map>[  spawned, <n> objects, <m> min left|until removed]
func ParseParks(text string) ([]SpawnedPark, bool) {
	lines := strings.Split(text, "\n")
	m := ParksHeadRe.FindStringSubmatch(lines[0])
	if m == nil {
		return nil, false
	}
	want, _ := strconv.Atoi(m[1]) // none set up: 0
	out := make([]SpawnedPark, 0, want)
	for _, l := range lines[1:] {
		if m := parkLineRe.FindStringSubmatch(strings.TrimPrefix(l, "  ")); m != nil {
			objects, _ := strconv.Atoi(m[2])
			minutes, _ := strconv.Atoi(m[3])
			out = append(out, SpawnedPark{Name: m[1], Objects: objects, Minutes: minutes})
		}
	}
	return out, len(out) == want
}
