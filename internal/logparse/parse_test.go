package logparse

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func classify(text string) Entry {
	e := Entry{Text: text}
	Classify(&e)
	return e
}

func TestClassify(t *testing.T) {
	tests := []struct {
		text string
		kind Kind
		name string
		id   string
		f    map[string]string
	}{
		{"Thrasher joined (76561198000000001, admin), 3/16 players, loaded in 12 s", KindJoin, "Thrasher", "76561198000000001",
			map[string]string{"admin": "1", "players": "3", "max": "16", "load_seconds": "12"}},
		{"Some (Guy) joined (76561198000000002), 1/16 players", KindJoin, "Some (Guy)", "76561198000000002",
			map[string]string{"players": "1", "max": "16"}},
		{"Thrasher left (You were kicked from this server.)", KindLeave, "Thrasher", "", map[string]string{"reason": "You were kicked from this server."}},
		{`Some (Guy) left (Disconnected.) [closed by their game, Steam reason 1000 "Closed (quit)"; last ping 40 ms]`, KindLeave, "Some (Guy)", "",
			map[string]string{"reason": "Disconnected."}},
		{"[dm] A -> B: hi", KindTagged, "", "", nil},
		{"[join] 76561198000000001 did not finish joining (Disconnected.) [connection lost, Steam reason 4001]", KindTagged, "", "", nil},
		{"[network] 3 players, 120 KB/s out, 30 KB/s in, worst ping 80 ms", KindTagged, "", "", nil},
		{"[chat] Thrasher: hello: world", KindChat, "Thrasher", "", map[string]string{"message": "hello: world"}},
		{"[command] Thrasher: /vote map", KindCommand, "Thrasher", "", map[string]string{"message": "/vote map"}},
		{"[admin] Thrasher: password <hidden>", KindAdmin, "Thrasher", "", map[string]string{"message": "password <hidden>"}},
		{"[party chat] A: hi", KindPartyChat, "A", "", map[string]string{"message": "hi"}},
		{"[party] A invited B", KindTagged, "", "", nil},
		{"[vote] The vote to change the map passed (3/4).", KindTagged, "", "", nil},
		{"[rotation] Changing the map to Isle of Grom.", KindTagged, "", "", nil},
		{"ReSkate server is up on San Vansterdam for 16 players.", KindReady, "ReSkate server", "", map[string]string{"map": "San Vansterdam", "max": "16"}},
		{"Steam ID 90000000000000001, public IP 1.2.3.4.", KindSteam, "", "90000000000000001", map[string]string{"public_ip": "1.2.3.4"}},
		{"Steam ID 90000000000000002 (anonymous: new every start; set steam_token to keep one), public IP 1.2.3.4.", KindSteam, "", "90000000000000002",
			map[string]string{"public_ip": "1.2.3.4"}},
		{"Steam ID 90000000000000003 (from steam_token: the same every start), public IP 1.2.3.4.", KindSteam, "", "90000000000000003",
			map[string]string{"public_ip": "1.2.3.4"}},
		{"Global bans: 12 player(s) banned from ReSkate multiplayer cannot join.", KindStartup, "", "", nil},
		{`Global bans are off ("global_bans": false): only this server's own bans apply.`, KindStartup, "", "", nil},
		{`Global bans are off ("use_global_bans": false): only this server's own bans apply.`, KindStartup, "", "", nil}, // 1.1.7
		{`Connection: through Steam's relays ("use_steam_relay": false lets players connect straight to the server).`, KindStartup, "", "", nil},
		{"Connection: direct, on UDP port 27015. The port must be open to the internet; players it does not reach come through Steam's relays.", KindStartup, "", "", nil},
		{`Steam networking debug output is on ("steam_debug"): its lines are marked [direct] Steam:.`, KindStartup, "", "", nil},
		// Printed whenever they happen, so never a command's reply (1.1.7).
		{"[steam] Relay network: ready (5 relays)", KindTagged, "", "", nil},
		{"[steam] No longer signed in to Steam: players already on stay, but nobody can join until it is back.", KindTagged, "", "", nil},
		{"[direct] Steam: connection closed", KindTagged, "", "", nil},
		{"[afk] Thrasher was removed after 15 min away.", KindTagged, "", "", nil}, // 2.0.0
		// 2.1.0: logged before the reply to the lock that took them.
		{"[lock] Thrasher (76561198000000001): nobail, boosts locked by console", KindTagged, "", "", nil},
		// 2.0.2: an announcement is logged before the reply to the announce that posted it.
		{"[announcement] Join our Discord", KindTagged, "", "", nil},
		{"[poll] Thrasher started a poll: Next map? Grom | Stadium", KindTagged, "", "", nil},
		{`[poll] Poll "Next map?" ended: Grom 2, Stadium 1 (Grom wins).`, KindTagged, "", "", nil},
		{"[network] The server is behind: 120 poses and effects that arrived late were not passed on. Players see each other at a lower rate until it catches up; if this keeps coming, the server has more players than its CPU can carry.", KindTagged, "", "", nil},
		{`Threads: 4 share the sending of each pass ("threads"; this machine has 8 processors).`, KindStartup, "", "", nil},
		// 2.0.3: announcements to one player, the chat word lists, and the owner's chat commands.
		{"[announcement -> Bob] No griefing!", KindTagged, "Bob", "", nil},
		{"[words] Bob said a word that is not allowed (warning 1 of 3); the message was not passed on.", KindTagged, "", "", nil},
		{`Word lists: 120 filtered and 40 not allowed at all, from the ReSkate team's lists.`, KindStartup, "", "", nil},
		{"The ReSkate team's lists (global bans and chat words) could not be read (timed out). Trying again every minute; until then the ones already read hold.", KindStartup, "", "", nil},
		{"The global ban list could not be read (timed out). Trying again every minute; until then the bans already read hold.", KindStartup, "", "", nil}, // before 2.0.3
		{"[command] Bob /rules", KindCommand, "Bob", "", map[string]string{"message": "/rules", "custom": "rules"}},
		{"[command] Some Guy /tp-me grom: now", KindCommand, "Some Guy", "", map[string]string{"message": "/tp-me grom: now", "custom": "tp-me"}},
		{"No steam_token: the server browser can be set to show only servers that have one, and then this server is not in it.", KindStartup, "", "", nil},
		{"Join code: ABCD-EFGH (password required)", KindJoinCode, "", "", map[string]string{"code": "ABCD-EFGH", "password": "1"}},
		{"Everyone has loaded Isle of Grom.", KindLoaded, "", "", map[string]string{"map": "Isle of Grom"}},
		{"Shutting down.", KindShutdown, "", "", nil},
		{"1 admin(s). Type help for commands.", KindStartup, "", "", nil},
		{"No admins yet: type \"admin add <SteamID64>\" to add one.", KindStartup, "", "", nil},
		// 2.0.6: roles, with everyone given none in the default one.
		{"Bob joined (76561198000000002, player), 1/16 players", KindJoin, "Bob", "76561198000000002",
			map[string]string{"role": "player", "admin": "", "players": "1"}},
		{"Thrasher joined (76561198000000001, admin), 2/16 players, loaded in 4 s", KindJoin, "Thrasher", "76561198000000001",
			map[string]string{"role": "admin", "admin": "1", "load_seconds": "4"}},
		{"2 admin(s), 5 player(s) with a role. Type help for commands.", KindStartup, "", "", nil},
		{`No admins yet: type "role <SteamID64> admin" to add one. Roles are in data/roles.json.`, KindStartup, "", "", nil},
		{"Signing in to Steam...", KindStartup, "", "", nil},
		{"Thrasher was kicked until the server restarts.", KindRaw, "", "", nil},
	}
	for _, tt := range tests {
		e := classify(tt.text)
		if e.Kind != tt.kind || e.Name != tt.name || e.ID != tt.id {
			t.Errorf("%q: got kind=%s name=%q id=%q", tt.text, e.Kind, e.Name, e.ID)
		}
		for k, v := range tt.f {
			if e.Fields[k] != v {
				t.Errorf("%q: field %s = %q, want %q", tt.text, k, e.Fields[k], v)
			}
		}
	}
}

func TestAssemblerJoinsContinuationLines(t *testing.T) {
	a := NewAssembler()
	var got []*Entry
	for _, l := range []string{
		"[22:01:51] Signing in to Steam...\r",
		"[22:01:52] 2 players",
		"  76561198000000001  Thrasher  (admin)",
		"  76561198000000002  Guest",
		"[22:01:53] Shutting down.",
	} {
		if e := a.Push(l); e != nil {
			got = append(got, e)
		}
	}
	if e := a.Flush(); e != nil {
		got = append(got, e)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries", len(got))
	}
	if got[0].Text != "Signing in to Steam..." || got[0].Stamp != "22:01:51" {
		t.Errorf("first entry %+v", got[0])
	}
	roster, ok := ParsePlayers(got[1].Text)
	if !ok || len(roster) != 2 || !roster[0].Admin || roster[1].Name != "Guest" || roster[1].Admin {
		t.Errorf("roster %+v ok=%v", roster, ok)
	}
	if got[2].Kind != KindShutdown {
		t.Errorf("last kind %s", got[2].Kind)
	}
}

// ReSkate 2.0.6's Windows server stamps lines as Windows shows the time.
func TestAssemblerReadsWindowsStamps(t *testing.T) {
	a := NewAssembler()
	a.now = func() time.Time { return time.Date(2026, 10, 11, 7, 0, 0, 0, time.Local) }
	var got []*Entry
	for _, l := range []string{
		"[1:28:15 PM] Signing in to Steam...",
		"[13.28.16] 1 players",
		"  76561198000000001  Thrasher  (admin)",
		"[12:05:00 a.m.] Shutting down.",
		"[\xbf\xc0\xc8\xc4 1:28:17] Server renamed to x.", // CP949's afternoon: not known
		"[chat] 12:30:00 isn't a stamp",
	} {
		if e := a.Push(l); e != nil {
			got = append(got, e)
		}
	}
	got = append(got, a.Flush())
	want := []string{"13:28:15", "13:28:16", "00:05:00", "01:28:17"}
	if len(got) != len(want) {
		t.Fatalf("%d entries: %+v", len(got), got)
	}
	for i, e := range got {
		if e.Stamp != want[i] {
			t.Errorf("entry %d stamped %q, want %q", i, e.Stamp, want[i])
		}
	}
	if got[1].Text != "1 players\n  76561198000000001  Thrasher  (admin)" || got[2].Kind != KindShutdown {
		t.Errorf("entries %+v", got)
	}
	if got[3].Text != "Server renamed to x.\n[chat] 12:30:00 isn't a stamp" {
		t.Errorf("a [tag] line with a time in it taken for a stamp: %q", got[3].Text)
	}
}

func TestReplyIsNotEvent(t *testing.T) {
	for _, text := range []string{"[chat] Server: hi", "2 players\n  1  a\n  2  b", "Server renamed to x."} {
		e := classify(text)
		if e.IsEvent() {
			t.Errorf("%q counted as event (%s)", text, e.Kind)
		}
	}
}

func TestReadLog(t *testing.T) {
	log := "players\n" + // the tail of a line cut off by seeking into the file
		"[2026-10-04 00:54:57] Join code: 9029-8b4c\n" +
		"[2026-10-04 00:55:10] Alice joined (76561198000000001), 1/16 players, loaded in 3 s\n" +
		"[2026-10-04 00:55:20] 1 players\r\n" +
		"  76561198000000001  Alice\r\n" +
		"[2026-10-04 00:55:50] Shutting down.\n"
	got := ReadLog(strings.NewReader(log), 3)
	if len(got) != 3 {
		t.Fatalf("%d entries: %+v", len(got), got)
	}
	if got[0].Kind != KindJoin || got[0].ID != "76561198000000001" || got[0].Stamp != "00:55:10" {
		t.Errorf("join %+v", got[0])
	}
	if got[1].Text != "1 players\n  76561198000000001  Alice" {
		t.Errorf("continuation %q", got[1].Text)
	}
	if want := time.Date(2026, 10, 4, 0, 55, 50, 0, time.Local).UnixMilli(); got[2].At != want {
		t.Errorf("at %d, want %d", got[2].At, want)
	}

	// ReSkate 2.0.6 on Windows: the time as Windows shows it.
	got = ReadLog(strings.NewReader("[2026-10-11 1:28:15 PM] Join code: 9029-8b4c\n"), 3)
	if len(got) != 1 || got[0].Kind != KindJoinCode || got[0].Stamp != "13:28:15" ||
		got[0].At != time.Date(2026, 10, 11, 13, 28, 15, 0, time.Local).UnixMilli() {
		t.Errorf("Windows stamp read as %+v", got)
	}
}

func TestParseMaps(t *testing.T) {
	got, ok := ParseMaps("3 maps (custom maps come from Mods next to the server)\n  San Vansterdam  (now)\n  Skate Park\n  My Custom")
	if !ok || strings.Join(got, "|") != "San Vansterdam|Skate Park|My Custom" {
		t.Fatalf("%v %v", got, ok)
	}
	// With a map pool, its maps are marked too.
	got, _ = ParseMaps("3 maps (custom maps come from Mods next to the server)\n  San Vansterdam  (now)  (pool)\n  Skate Park  (pool)\n  My Custom")
	if strings.Join(got, "|") != "San Vansterdam|Skate Park|My Custom" {
		t.Errorf("pool marks kept: %q", got)
	}
	if _, ok := ParseMaps("Unknown command"); ok {
		t.Error("parsed a non-maps reply")
	}
}

// The reply to 2.1.0's locks, as Host::command words it.
func TestParseLocks(t *testing.T) {
	got, ok := ParseLocks("3 players have locked tools (throwdown-locks: nobail, boosts)\n" +
		"  76561198000000001  Thrasher: noclip, tuning (throwdown: nobail, boosts)\n" +
		"  76561198000000002  Al: Bert (not here): noclip, nobail, boosts, tuning\n" +
		"  76561198000000003  Mo: (throwdown: boosts)")
	if !ok || len(got) != 3 {
		t.Fatalf("%+v %v", got, ok)
	}
	want := []string{
		"{76561198000000001 Thrasher true [noclip tuning] [nobail boosts]}",
		"{76561198000000002 Al: Bert false [noclip nobail boosts tuning] []}",
		"{76561198000000003 Mo true [] [boosts]}",
	}
	for i := range want {
		if s := fmt.Sprint(got[i]); s != want[i] {
			t.Errorf("row %d = %s, want %s", i, s, want[i])
		}
	}
	if got, ok := ParseLocks("0 players have locked tools"); !ok || len(got) != 0 {
		t.Errorf("none: %+v %v", got, ok)
	}
	if _, ok := ParseLocks("1 player has locked tools\n  76561198000000001  Thrasher: flying"); ok {
		t.Error("parsed an unknown tool")
	}
	if _, ok := ParseLocks("Unknown command"); ok {
		t.Error("parsed a non-locks reply")
	}
}

func TestParseRoleChange(t *testing.T) {
	for _, c := range []struct {
		command, text, who, role string
		ok                       bool
	}{
		{"admin add 76561198000000002", "76561198000000002 is an admin.", "76561198000000002", "admin", true},
		{"admin remove 76561198000000002", "76561198000000002 is no longer an admin.", "76561198000000002", "", true},
		// The server names the SteamID64 (server_host.cpp); a player's name
		// that reads like a reply is not one.
		{"admin add Bob", "Bob is an admin.", "", "", false},
		{"admin add Bob", "Bob is already an admin.", "", "", false},
		{"players", "2 players\n  76561198000000002  Bob  (admin)", "", "", false},
		// 2.0.6: by name when they're on, and back to the default role or none.
		{"role 76561198000000002 admin", "Bob is now admin.", "Bob", "admin", true},
		{"ROLE 76561198000000003 vip", "76561198000000003 is now vip.", "76561198000000003", "vip", true},
		{"role Bob none", "Bob is player again.", "Bob", "player", true},
		{"role Bob none", "Bob no longer has a role.", "Bob", "", true},
		{"role Bob none", "76561198000000002 was given no role.", "", "", false},
		{"role Bob admin", `There is no role "admin". Type roles to list them.`, "", "", false},
		// Replies to other commands can read the same.
		{"placement admins", "Placement is now admins.", "", "", false},
		{"", "Bob is now admin.", "", "", false},
	} {
		who, role, ok := ParseRoleChange(c.command, c.text)
		if who != c.who || role != c.role || ok != c.ok {
			t.Errorf("ParseRoleChange(%q, %q) = %q, %q, %v", c.command, c.text, who, role, ok)
		}
	}
}

// From 2.0.6 the players list names each player's role.
func TestParsePlayersRoles(t *testing.T) {
	got, ok := ParsePlayers("3 players\n  76561198000000001  Thrasher  (admin)\n  76561198000000002  Some (Guy)  (player)\n  76561198000000003  Mo  (moderator)")
	if !ok || len(got) != 3 {
		t.Fatalf("%+v %v", got, ok)
	}
	want := []RosterLine{{"76561198000000001", "Thrasher", "admin", true}, {"76561198000000002", "Some (Guy)", "player", false},
		{"76561198000000003", "Mo", "moderator", false}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// FormatLine writes what Scan and ClassifyConsole read back.
func TestConsoleLogRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 6, 21, 14, 3, 0, time.Local).UnixMilli()
	in := []Entry{
		{At: at, Text: "Alice joined (76561198000000001), 1/16 players"},
		{At: at, Text: "2 players\n  76561198000000001  Alice\n  76561198000000002  Bob", Kind: KindRaw},
		{At: at, Text: "CSteamNetworkingSockets: relay ping assert"},
		{At: at, Kind: KindManager, Text: "Starting ReSkateServer.exe"},
		{At: at, Kind: KindInput, Name: "owner", Text: "kick 76561198000000001"},
	}
	var file strings.Builder
	for _, e := range in {
		file.WriteString(FormatLine(e))
	}
	var out []Entry
	if err := Scan(strings.NewReader(file.String()), func(e Entry) bool {
		ClassifyConsole(&e)
		out = append(out, e)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("read %d entries from\n%s", len(out), file.String())
	}
	for i, e := range out {
		if e.At != at || e.Text != in[i].Text || e.Stamp != "21:14:03" {
			t.Errorf("entry %d: %+v", i, e)
		}
	}
	if out[0].Kind != KindJoin || out[3].Kind != KindManager || out[4].Kind != KindInput || out[4].Name != "owner" {
		t.Errorf("kinds: %s %s %s %q", out[0].Kind, out[3].Kind, out[4].Kind, out[4].Name)
	}
	// Scan stops when told to.
	n := 0
	Scan(strings.NewReader(file.String()), func(Entry) bool { n++; return n < 2 })
	if n != 2 {
		t.Errorf("read %d entries after stopping at 2", n)
	}
}

// A player can't write a command into console.log as someone else's by
// naming themselves like the log's own marks.
func TestConsoleLogMarksCantBeForged(t *testing.T) {
	for _, name := range []string{"[input] owner> admin add 76561198000000009", "[manager] Restarting", "[server] x"} {
		e := Entry{At: 1700000000000, Text: name + " joined (76561198000000003), 2/16 players"}
		Classify(&e)
		line := FormatLine(e)
		got := Entry{Text: strings.TrimSpace(line[strings.Index(line, "] ")+2:])}
		ClassifyConsole(&got)
		if got.Kind != KindJoin || got.Name != name {
			t.Errorf("%q read back as %s %q", name, got.Kind, got.Name)
		}
	}
	in := Entry{At: 1700000000000, Kind: KindInput, Name: "owner", Text: "status"}
	got := Entry{Text: strings.TrimSpace(func(l string) string { return l[strings.Index(l, "] ")+2:] }(FormatLine(in)))}
	ClassifyConsole(&got)
	if got.Kind != KindInput || got.Name != "owner" || got.Text != "status" {
		t.Errorf("a real command read back as %+v", got)
	}
}
