// Package serverconfig reads and changes ReSkateServer.json.
//
// The server reads the file only at startup and writes it back after every
// setting command, so while it runs the manager changes settings with console
// commands (which apply at once and persist) and never writes the file. A few
// keys have no command; those are written while the server is stopped.
// Field rules mirror Server/server_config.cpp (load_config, config_error) and
// the command handlers in Server/server_host.cpp.
package serverconfig

import "strconv"

// FieldType is what kind of value a setting holds.
type FieldType string

// The kinds of setting, as the settings page draws them.
const (
	TypeString FieldType = "string"
	TypeInt    FieldType = "int"
	TypeNumber FieldType = "number"
	TypeBool   FieldType = "bool"
	TypeEnum   FieldType = "enum"
	TypeList   FieldType = "list" // list of strings
	TypeMaps   FieldType = "maps" // ordered list of map names
)

// Field describes one setting of ReSkateServer.json for the settings page
// and for checking a value before it is saved.
type Field struct {
	Key     string    `json:"key"` // dotted path into the JSON
	Label   string    `json:"label"`
	Group   string    `json:"group"`
	Type    FieldType `json:"type"`
	Options []string  `json:"options,omitempty"`
	Min     float64   `json:"min,omitempty"`
	Max     float64   `json:"max,omitempty"`
	MaxLen  int       `json:"maxLen,omitempty"`
	Help    string    `json:"help,omitempty"`
	// Info is the longer explanation behind the field's info hover (info.go).
	Info string `json:"info,omitempty"`
	// Restart: no console command exists, so the change is written while the server is stopped.
	Restart bool `json:"restart,omitempty"`
	Secret  bool `json:"secret,omitempty"`
	// Owner: only panel owners see or change it (see File.Redact).
	Owner bool `json:"owner,omitempty"`
	// Long text gets a box that wraps; it is still one line.
	Long bool `json:"long,omitempty"`
	// Default is the server's own default (ServerConfig in Server/server_config.h),
	// used for keys a config written before the first start does not have yet.
	Default any `json:"default"`
}

var parkLayouts = func() map[string][]string {
	families := []string{"flumppark", "megapark", "skatepark", "streetpark"}
	counts := map[string][4]int{
		"construction": {8, 4, 4, 9},
		"historic":     {10, 5, 7, 6},
		"financial":    {8, 4, 7, 6},
	}
	out := map[string][]string{}
	for lot, c := range counts {
		opts := []string{"empty"}
		for f, fam := range families {
			for v := 1; v <= c[f]; v++ {
				id := fam + "_"
				if v < 10 {
					id += "0"
				}
				id += strconv.Itoa(v)
				opts = append(opts, id)
			}
		}
		out[lot] = opts
	}
	return out
}()

var checkModes = []string{"off", "warn", "kick"}

// Fields is the editable schema, in display order.
var Fields = []Field{
	{Key: "name", Label: "Server name", Group: "General", Type: TypeString, MaxLen: 64, Help: "Shown in the server browser. Letters, numbers, spaces and - _ / [ ] ( ) only.", Default: defaultServerName},
	{Key: "map", Label: "Map", Group: "General", Type: TypeString, MaxLen: 256, Help: "A retail map, or a custom map from the server's Mods folder (see the Mods tab).", Default: "San Vansterdam"},
	{Key: "max_players", Label: "Max players", Group: "General", Type: TypeInt, Min: 1, Max: 249, Restart: true, Default: 16.0},
	{Key: "password", Label: "Password", Group: "General", Type: TypeString, MaxLen: 64, Secret: true, Help: "Empty: anyone may join.", Default: ""},
	{Key: "welcome", Label: "Welcome message", Group: "General", Type: TypeString, MaxLen: 200, Long: true, Help: "Sent to each player as they join.", Default: ""},
	{Key: "listed", Label: "Listed in the server browser", Group: "General", Type: TypeBool, Default: true},
	{Key: "steam_token", Label: "Steam login token", Group: "General", Type: TypeString, MaxLen: 64, Secret: true, Owner: true, Restart: true, Help: "The server browser only shows servers that have one. Each server needs its own.", Default: ""},

	{Key: "reserved_slots", Label: "Reserved slots", Group: "Reserved slots", Type: TypeInt, Min: 0, Max: 248, Help: "Of the max players, kept for the players below and the admins. 0 is none.", Default: 0.0},
	{Key: "reserved", Label: "Players with a reserved slot", Group: "Reserved slots", Type: TypeList, Help: "SteamID64s. They can join a full server; admins always can.", Default: []string{}},

	{Key: "map_pool", Label: "Maps in the pool", Group: "Map pool", Type: TypeMaps, Help: "The maps players vote between, in rotation order.", Default: []string{}},
	{Key: "map_rotation_minutes", Label: "Minutes on each map", Group: "Map pool", Type: TypeInt, Min: 0, Max: 1440, Help: "Then the server moves to the next pool map. 0 is off.", Default: 0.0},

	{Key: "use_steam_relay", Label: "Steam relays only", Group: "Network", Type: TypeBool, Restart: true, Help: "Off lets players connect straight to the game port, which must then be open (UDP).", Default: true},
	{Key: "send_rate", Label: "Send rate per player (KB/s)", Group: "Network", Type: TypeInt, Min: 128, Max: 16384, Help: "Steam's relays carry about 1100; above that, more is lost.", Default: 900.0},
	{Key: "crowd_budget", Label: "Crowd budget (updates a second)", Group: "Network", Type: TypeInt, Min: 0, Max: 20000, Help: "The most position updates one player is sent. 0 is no limit; otherwise 300 to 20000.", Default: 600.0},
	{Key: "pack_ms", Label: "Packing delay (ms)", Group: "Network", Type: TypeInt, Min: 0, Max: 50, Restart: true, Help: "How long a message may wait to share a packet. 0 sends each at once.", Default: 10.0},
	{Key: "finger_distance", Label: "Finger distance (m)", Group: "Network", Type: TypeInt, Min: 0, Max: 10000, Restart: true, Help: "Past this, a player's fingers aren't sent moving. 0 always sends them.", Default: 25.0},
	{Key: "port", Label: "Game port (UDP)", Group: "Network", Type: TypeInt, Min: 1, Max: 65535, Restart: true, Default: 27015.0},
	{Key: "query_port", Label: "Query port (UDP)", Group: "Network", Type: TypeInt, Min: 1, Max: 65535, Restart: true, Default: 27016.0},
	{Key: "distances.full_rate_return", Label: "Full rate return (m)", Group: "Network", Type: TypeInt, Min: 0, Max: 10000, Default: 50.0},
	{Key: "distances.half_rate_start", Label: "Half rate start (m)", Group: "Network", Type: TypeInt, Min: 0, Max: 10000, Default: 60.0},
	{Key: "distances.half_rate_return", Label: "Half rate return (m)", Group: "Network", Type: TypeInt, Min: 0, Max: 10000, Default: 150.0},
	{Key: "distances.low_rate_start", Label: "Low rate start (m)", Group: "Network", Type: TypeInt, Min: 0, Max: 10000, Default: 170.0},
	{Key: "steam_debug", Label: "Steam networking log", Group: "Network", Type: TypeBool, Restart: true, Help: "Logs what Steam's networking does, to find why players can't connect. A lot of text: turn it off after.", Default: false},

	{Key: "voice_chat", Label: "Voice chat", Group: "Voice", Type: TypeBool, Default: true},
	{Key: "voice_range", Label: "Voice range (m)", Group: "Voice", Type: TypeNumber, Min: 50, Max: 1000, Default: 300.0},

	{Key: "object_placement", Label: "Who may place objects", Group: "Gameplay", Type: TypeEnum, Options: []string{"everyone", "admins", "nobody"}, Default: "everyone"},
	{Key: "object_limit", Label: "Objects each player may place", Group: "Gameplay", Type: TypeInt, Min: 0, Max: 1024, Help: "0 is no limit. Admins are not limited.", Default: 100.0},
	{Key: "allow_object_scaling", Label: "Players may resize objects", Group: "Gameplay", Type: TypeBool, Help: "Off shares every placed object at its own size. Admins always can.", Default: true},
	{Key: "bone_scale_limit", Label: "Resized body parts, at most (times)", Group: "Gameplay", Type: TypeNumber, Min: 0, Max: 8, Help: "How far a mod may resize part of a skater for the others. 0 is no limit.", Default: 2.0},
	{Key: "sync_effects", Label: "Players see each other's skater effects", Group: "Gameplay", Type: TypeBool, Help: "Sparks, dust, and the trails and fire of costumes and boards.", Default: true},
	{Key: "noclip", Label: "Players may noclip and teleport", Group: "Gameplay", Type: TypeBool, Default: true},
	{Key: "no_bail", Label: "Players may use No Bail", Group: "Gameplay", Type: TypeBool, Default: true},
	{Key: "boosts", Label: "Players may use boosts", Group: "Gameplay", Type: TypeBool, Default: true},
	{Key: "enforce_tuning", Label: "Enforce the game's physics tuning", Group: "Gameplay", Type: TypeBool, Default: true},
	{Key: "parties", Label: "Parties", Group: "Gameplay", Type: TypeBool, Default: true},
	{Key: "party_size", Label: "Party size", Group: "Gameplay", Type: TypeInt, Min: 2, Max: 8, Default: 8.0},
	{Key: "afk_kick_minutes", Label: "Remove players away for (min)", Group: "Gameplay", Type: TypeInt, Min: 0, Max: 1440, Help: "They're warned a minute before. 0 is never. Admins are never removed.", Default: 0.0},
	{Key: "announce_throwdowns", Label: "Announce throwdowns in chat", Group: "Gameplay", Type: TypeBool, Default: true},
	{Key: "activity_log", Label: "Log player activity", Group: "Gameplay", Type: TypeBool, Default: true},

	{Key: "parks.construction", Label: "Construction site / Hedgemont", Group: "Parks", Type: TypeEnum, Options: parkLayouts["construction"], Default: "skatepark_01"},
	{Key: "parks.historic", Label: "Piers 1 / Historic", Group: "Parks", Type: TypeEnum, Options: parkLayouts["historic"], Default: "megapark_05"},
	{Key: "parks.financial", Label: "Piers 2 / Financial", Group: "Parks", Type: TypeEnum, Options: parkLayouts["financial"], Default: "flumppark_08"},

	{Key: "votes.map.enabled", Label: "Map votes", Group: "Votes", Type: TypeBool, Default: false},
	{Key: "votes.map.percent", Label: "Map vote % to pass", Group: "Votes", Type: TypeInt, Min: 1, Max: 100, Default: 60.0},
	{Key: "votes.kick.enabled", Label: "Kick votes", Group: "Votes", Type: TypeBool, Default: false},
	{Key: "votes.kick.percent", Label: "Kick vote % to pass", Group: "Votes", Type: TypeInt, Min: 1, Max: 100, Default: 60.0},
	{Key: "votes.time_of_day.enabled", Label: "Time of day votes", Group: "Votes", Type: TypeBool, Help: "Needs world layer sync.", Default: false},
	{Key: "votes.time_of_day.percent", Label: "Time of day vote % to pass", Group: "Votes", Type: TypeInt, Min: 1, Max: 100, Default: 50.0},
	{Key: "votes.seconds", Label: "Vote length (s)", Group: "Votes", Type: TypeInt, Min: 10, Max: 300, Default: 30.0},
	{Key: "votes.cooldown_seconds", Label: "Cooldown between a player's votes (s)", Group: "Votes", Type: TypeInt, Min: 0, Max: 3600, Default: 60.0},

	{Key: "global_bans", Label: "Turn away players the ReSkate team banned", Group: "Anti-cheat", Type: TypeBool, Restart: true, Help: "The server's own bans apply either way.", Default: true},
	{Key: "speed_check", Label: "Speedhack check", Group: "Anti-cheat", Type: TypeEnum, Options: checkModes, Default: "warn"},
	{Key: "score_check", Label: "Scoring mod check", Group: "Anti-cheat", Type: TypeEnum, Options: checkModes, Default: "warn"},
	{Key: "score_allow", Label: "Allowed scoring fingerprints", Group: "Anti-cheat", Type: TypeList, Help: "16 hex digits each, as the console prints them.", Default: []string{}},

	{Key: "world_layer_sync", Label: "World layer sync", Group: "World", Type: TypeBool, Help: "Needs world-layers.json next to the server.", Default: false},
}

// OwnerOnly reports whether only owners may see or change the setting.
func OwnerOnly(key string) bool {
	fd, ok := FieldByKey(key)
	return ok && fd.Owner
}

// FieldByKey finds a setting's description by its key.
func FieldByKey(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}
