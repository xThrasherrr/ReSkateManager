package serverconfig

// The settings page's info hovers. Behaviour comes from Server/README.txt,
// Server/server_votes.cpp and pose_interval in Extension/Multiplayer/Session/room.h.

// GroupInfo explains a settings group as a whole, beside its heading.
var GroupInfo = map[string]string{
	"Network": "Far-away players are sent fewer updates, saving bandwidth on skaters you can barely see:\n" +
		"• Close by: the full rate, 20 updates a second.\n" +
		"• Past half rate start: 10 updates a second.\n" +
		"• Past low rate start: 5 updates a second.\n" +
		"Each step has a separate start and return distance, so a player at a boundary doesn't flicker between rates. " +
		"The four distances must rise in order: full rate return < half rate start ≤ half rate return < low rate start.\n" +
		"A player who has stood still for a few seconds (in a menu, or away) is sent at 5 a second to everyone until they move.",
	"Parks": "San Vansterdam has three park lots. Each shows one layout to everyone: a Flump, Mega, Skate or Street park variant, " +
		"or \"empty\" for a bare lot. Changes apply live.",
	"Map pool": "Build a server around a theme, like only Skate 3 maps. An empty pool is every map the server knows.\n" +
		"• Map votes take only the pool's maps, and players' map suggestions list only those.\n" +
		"• The rotation goes down the pool from the map the server is on, or from the top when that map isn't in it.\n" +
		"Admins can still change to any map. In-game admins can also edit the pool under Multiplayer > Session, or with /map-pool and /rotation in chat; their changes show here.",
	"Votes": "Players start a vote in chat:\n" +
		"• /vote map <map>: change the map (only maps in the map pool).\n" +
		"• /vote kick <player>: kick a player until the server restarts.\n" +
		"• /vote tod <time>: set the time of day.\n" +
		"Everyone else answers /yes or /no, and the starter counts as yes. Only one vote runs at a time.\n" +
		"The pass percentage is of everyone connected, not of those who voted. A vote ends early as soon as it passes or can no longer pass.",
}

var fieldInfo = map[string]string{
	"map":         "Lists the retail maps and every map in the server's Mods folder. Players need the same custom map mod installed to join. The server reads Mods when it starts, so a map added since needs a restart first. Changing it live moves everyone to the new map.",
	"max_players": "The server itself doesn't take a slot. Changing it means a restart, which disconnects everyone.",
	"listed":      "Off hides the server from the in-game browser; players then join with its code. The code changes every time the server starts.",
	"steam_token": "A Steam game server login token. Make one at steamcommunity.com/dev/managegameservers with App ID 3354750. Each running server needs its own.\n" +
		"With a token the server keeps the same Steam ID every start. Without one it signs in anonymously, and the ReSkate team's server browser can hide it; players can still join with its code.\n" +
		"Keep it private: anyone with it can sign in as your server. The server reads it only when it starts.",

	"reserved_slots": "With 64 max players and 4 reserved slots, anyone can join until 60 are on; the last 4 only reserved players and admins can take. " +
		"The server browser still shows 64, and everyone else is told the remaining slots are reserved. It must be less than max players. " +
		"ReSkate 1.1.7 took this setting out: since then the players below get extra slots instead.",
	"reserved": "SteamID64s (17 digits starting 7656119), one per line, up to 1024. The Players page shows each player's. " +
		"From ReSkate 1.1.8, anyone can join until max players are on, and then these players and the admins still can, each adding a slot beyond the limit (33/32 and so on). " +
		"Before that, they took the reserved slots above. Admins always have a place, so they needn't be listed.",

	"map_pool": "The maps players vote between and the rotation goes through, top to bottom. With none picked, it's every map: the retail ones and every custom map in Mods.\n" +
		"Keep each pool map's mod installed: the server won't start while the pool names a map it doesn't have.",
	"map_rotation_minutes": "After this long on a map, the server moves everyone to the next map in the pool, with a minute's warning in chat.\n" +
		"The clock waits while nobody is on, starts over whenever the map changes (by a vote or an admin too), and holds back while a map vote runs. 0 turns it off; the most is 1440 (a day).",

	"send_rate": "The most the server sends each player a second. About 1100 KB/s is what reaches a player through Steam's relays: set higher, what's lost is resent until the connection is full of resends and half of everything is lost.\n" +
		"A change also applies to the players already on.",
	"crowd_budget": "The most position updates one player is sent a second. Players near each other normally get the full 20 a second, so this only matters once more than budget ÷ 20 are in one place: 30 at the default 600.\n" +
		"Then the nearest stay at the full rate and the farthest of the crowd drop to 10 and 5 a second, instead of everyone's connection filling up. About 1000 is what reaches a player through Steam's relays; more than that was seen to lose half. 0 is no limit.",
	"port":                       "The UDP port Steam uses for game traffic. Players connect through Steam's relay network, so it doesn't need opening. Forwarding it with the query port shows the server's ping in the browser and makes joining a little faster.\nEach server needs its own port pair.",
	"query_port":                 "The UDP port the Steam server browser asks for the server's name, players and ping. It must differ from the game port, and only needs forwarding along with it.",
	"distances.full_rate_return": "Players closer than this always get the full rate. A player on a reduced rate goes back to full once they come this close.",
	"distances.half_rate_start":  "Past this distance a player drops from the full tick rate to 10 updates a second.",
	"distances.half_rate_return": "A player on the low rate (5 a second) goes back up to 10 a second once they come this close.",
	"distances.low_rate_start":   "Past this distance a player drops to 5 updates a second.",

	"voice_chat":  "Off turns voice chat off for everyone on the server.",
	"voice_range": "How far the server carries proximity voice. Players still pick their own hearing distance in the game, within this range.",

	"object_placement":    "\"admins\" lets only in-game admins (from the Players page) place objects. The console's clear-objects removes everything placed.",
	"object_limit":        "How many objects one player can have placed at a time, 1 to 1024. A player at the limit deletes one of theirs to place another. Admins are never limited.",
	"bone_scale_limit":    "The most a mod may scale one part of a skater, such as a big-head mod, as others see it: 1 to 8 times, or 0 for no limit. The default is 2: the game's own skater height is a bone scale, so at 1 every skater shows at one height. The player with the mod still sees it on their own screen. Needs ReSkate 1.1.6.",
	"noclip":              "Lets players fly with noclip and teleport to others with /tp. Admins always can.",
	"no_bail":             "Lets players turn on No Bail, which stops them wiping out. Admins always can.",
	"boosts":              "Lets players use the forward and up boosts. Admins always can.",
	"enforce_tuning":      "Everyone skates with the game's own Gameplay/SkatePhysicsTuning. Off allows edited copies (truck positions and the rest), and those edits show on that skater for everyone.",
	"parties":             "Players invite each other from the game's Social menu, a player card or chat (/party invite <player>). Party members join each other's coop challenges, see each other on the map and talk with /p <message>.",
	"party_size":          "The most players in one party, 2 to 8.",
	"announce_throwdowns": "Tells everyone in chat when a throwdown drop is placed.",
	"activity_log":        "Writes console lines for throwdowns (drops placed, joins, starts, turns and results), objects placed or removed, and how long players take to load.",

	"votes.map.enabled":         "Lets players start /vote map <map>. They can pick from the map pool only, or from all the server's maps while the pool is empty.",
	"votes.map.percent":         "The share of connected players whose yes passes a map vote, rounded up. With 10 players on at 60%, 6 must vote yes.",
	"votes.kick.enabled":        "Lets players start /vote kick <player>. A passed kick lasts until the server restarts. Admins can't be vote-kicked.",
	"votes.kick.percent":        "The share of connected players whose yes passes a kick vote, rounded up. The player being kicked doesn't count or vote, and a kick always needs at least two yes votes.",
	"votes.time_of_day.enabled": "Lets players start /vote tod <time>: morning, noon, afternoon, evening, night, weatherday, weathernight or default. Only works while World layer sync is on.",
	"votes.time_of_day.percent": "The share of connected players whose yes passes a time of day vote, rounded up.",
	"votes.seconds":             "How long a vote stays open. One that hasn't passed by then fails.",
	"votes.cooldown_seconds":    "After starting a vote, a player waits this long before starting another. They can still vote on other players' votes.",

	"global_bans": "Players the ReSkate team has banned from multiplayer are refused when they join, and removed if they're already on. The server reads the list from api.reskate.dev when it starts and every ten minutes after.\n" +
		"Off lets them in. Bans from the Players page apply either way.",
	"speed_check": "Catches players whose game runs faster than normal (Cheat Engine's speedhack and the like), judged by the timing of what their game sends.\n" +
		"• warn: takes them out of throwdowns and coop challenges until their speed is normal again, and tells the admins.\n" +
		"• kick: removes them from the server.\n" +
		"• off: no check.",
	"score_check": "Catches players whose mods change trick scoring or how the skater handles. Each player's ReSkate checks its mods at launch and reports when joining.\n" +
		"• warn: takes them out of throwdowns and coop challenges and tells everyone in chat.\n" +
		"• kick: removes them from the server.\n" +
		"• off: no check.\n" +
		"A flagged player has to restart Skate without the mod to take part again.",
	"score_allow": "For a server that runs on a scoring mod everyone installs: fingerprints listed here count as the game's own. A flagged player's fingerprint is printed in the console.",

	"world_layer_sync": "On: every player sees the server's world layers (time of day and the rest, set on the World page) and can't change them. Off: players choose their own.\n" +
		"Needs world-layers.json next to the server. Copy it from a player's %LOCALAPPDATA%\\ReSkate\\cache folder, from the same game build. Time of day votes need this on.",
}

func init() {
	for i := range Fields {
		Fields[i].Info = fieldInfo[Fields[i].Key]
	}
}
