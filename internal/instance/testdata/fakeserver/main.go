// A stand-in for ReSkateServer: the same stdout format, startup lines and a
// few commands, so the instance package can be tested without Steam.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

func log(text string) { fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), text) }

// slowAdmin is made an admin (or given a role) at once, but the reply comes
// a second later.
const slowAdmin = "76561198000000099"

// validName is valid_server_name from Engine/Game/Multiplayer/session_model.h.
func validName(name string) bool {
	if name == "" || len(name) > 64 || name[0] == ' ' || name[len(name)-1] == ' ' {
		return false
	}
	named := false
	for _, c := range []byte(name) {
		word := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
		if !word && !strings.ContainsRune(" -_/[]()", rune(c)) {
			return false
		}
		named = named || word
	}
	return named
}

func main() {
	log("Signing in to Steam...")
	exe, _ := os.Executable()
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "fail")); err == nil {
		log("Config problem: name must be 1 to 64 characters.")
		os.Exit(1)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "ReSkateServer.json")); err == nil {
		var cfg struct{ Name *string }
		json.Unmarshal(data, &cfg)
		if cfg.Name != nil && !validName(*cfg.Name) {
			log("Config problem: name must be 1 to 64 letters, numbers, spaces and - _ / [ ] ( ).")
			os.Exit(1)
		}
	}
	// "mute" beside it: like 2.0.6's headless Windows server, it takes its
	// console for one of its own and runs on unheard.
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "mute")); err == nil {
		os.Stdout.Close()
		os.Stderr.Close()
		os.Stdin.Close()
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	log("Fake is up on San Vansterdam for 16 players.")
	log("Steam ID 90000000000000001, public IP 1.2.3.4.")
	log("Join code: CODE-1")
	// data/roles.json beside it: a server from ReSkate 2.0.6, with roles.
	rolesPath := filepath.Join(filepath.Dir(exe), "data", "roles.json")
	var roles map[string]any
	if data, err := os.ReadFile(rolesPath); err == nil {
		json.Unmarshal(data, &roles)
	}
	given := func(id string) string {
		players, _ := roles["players"].(map[string]any)
		if role, ok := players[id].(string); ok {
			return role
		}
		s, _ := roles["default_role"].(string)
		return s
	}
	if roles != nil {
		log("1 admin(s), 1 player(s) with a role. Type help for commands.")
		log("Chat words: the ReSkate team's lists are used.") // 2.0.6 prints what words answers, after it's up
	} else {
		log("1 admin(s). Type help for commands.")
	}
	players := map[string]string{}
	banned := map[string]bool{}
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		line := in.Text()
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "quit":
			log("Shutting down.")
			return
		case "steam": // test hook: a line Steam prints, unstamped and never in the server's log
			fmt.Println("CSteamNetworkingSockets: relay ping assert")
			time.Sleep(300 * time.Millisecond)
			log("ok")
		case "status":
			log(fmt.Sprintf("Fake | San Vansterdam | %d/16 players | 30 TPS | voice on (300 m) | password off | code CODE-1", len(players)))
		case "join": // test hook: "join <id> <name>"
			id, name, _ := strings.Cut(arg, " ")
			players[id] = name
			if role := given(id); role != "" {
				id += ", " + role
			}
			log(fmt.Sprintf("%s joined (%s), %d/16 players, loaded in 3 s", name, id, len(players)))
			log("ok")
		case "leave":
			name := players[arg]
			delete(players, arg)
			log(name + " left (Disconnected)")
			log("ok")
		case "vanish": // test hook: a player gone without a leave line
			delete(players, arg)
			log("ok")
		case "crash": // test hook: the server going down on its own
			os.Exit(3)
		case "emit": // test hook: a line of the server's own, as "emit [throwdown] ..."
			log(arg)
			log("ok")
		case "typed": // test hook: "typed <runs> <name> /<word> [text]", a player's chat command as the server logs it
			runs, rest, _ := strings.Cut(arg, " ")
			name, line, _ := strings.Cut(rest, " /")
			log("[command] " + name + ": /" + line)
			log("[command] " + name + " /" + line)
			n, _ := strconv.Atoi(runs)
			for range n {
				log("Fake | San Vansterdam | 1/16 players") // what status says
			}
			log("ok")
		case "role": // "role <id> <role>|none", saved to data/roles.json like the real server
			id, role, _ := strings.Cut(arg, " ")
			label := id
			if name, on := players[id]; on {
				label = name
			}
			list, _ := roles["players"].(map[string]any)
			reply := label + " is now " + role + "."
			if role == "none" {
				delete(list, id)
				reply = label + " is " + given(id) + " again."
			} else {
				list[id] = role
			}
			data, _ := json.MarshalIndent(roles, "", "  ")
			os.WriteFile(rolesPath, data, 0o644)
			if id == slowAdmin { // test hook: done at once, said late
				time.Sleep(time.Second)
			}
			log(reply)
		case "given": // test hook: "given <id> <role>", a role an admin gave in game, answered to them alone
			id, role, _ := strings.Cut(arg, " ")
			roles["players"].(map[string]any)[id] = role
			log("[admin] Alice: role " + id + " " + role)
			log("ok")
		case "admin": // "admin add|remove <id>", saved to ReSkateServer.json like the real server
			if roles != nil {
				log("Admins are a role now: roles lists them, and role <player or SteamID64> admin makes one (role ... none takes it away).")
				break
			}
			sub, id, _ := strings.Cut(arg, " ")
			path := filepath.Join(filepath.Dir(exe), "ReSkateServer.json")
			root := map[string]any{}
			if data, err := os.ReadFile(path); err == nil {
				json.Unmarshal(data, &root)
			}
			var admins []string
			if list, ok := root["admins"].([]any); ok {
				for _, v := range list {
					admins = append(admins, fmt.Sprint(v))
				}
			}
			admins = slices.DeleteFunc(admins, func(x string) bool { return x == id })
			reply := id + " is no longer an admin."
			if sub == "add" {
				admins, reply = append(admins, id), id+" is an admin."
			}
			root["admins"] = admins
			data, _ := json.MarshalIndent(root, "", "  ")
			os.WriteFile(path, data, 0o644)
			if id == slowAdmin { // test hook: done at once, said late
				time.Sleep(time.Second)
			}
			log(reply)
		case "words": // 2.0.6
			log("Chat words: the ReSkate team's lists are " + map[bool]string{true: "off", false: "used"}[arg == "global off"] + ".")
		case "park-mod": // 2.0.6: the console's reply is the line logged for an admin in the game
			log("[parks] " + arg + " by console")
		case "parks": // test hook: a park that runs out, logged on its own
			log("[parks] " + arg + " removed (expired)")
		case "say":
			log("[chat] Server: " + arg)
		case "msg": // "msg <id> <text>", to one player
			id, _, _ := strings.Cut(arg, " ")
			if name, on := players[id]; on {
				log("Sent to " + name + ".")
			} else {
				log("No single connected player matches \"" + id + "\".")
			}
		case "announce": // 2.0.2; test hook: a file "before-2.0.2" beside it makes it older
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "before-2.0.2")); err == nil {
				log("Unknown command \"" + verb + "\". Type help.")
				break
			}
			log("[announcement] " + arg)
			log("Announced.")
		case "announce-to": // "announce-to <id> <text>", 2.0.3
			id, text, _ := strings.Cut(arg, " ")
			if name, on := players[id]; on && text != "" {
				log("[announcement -> " + name + "] " + text)
				log("Announced to " + name + ".")
			} else {
				log("No single connected player matches \"" + id + "\".")
			}
		case "kick": // "kick <id> [minutes|restart]"
			id, _, _ := strings.Cut(arg, " ")
			name, on := players[id]
			if !on {
				log("No single connected player matches \"" + arg + "\".")
				break
			}
			delete(players, id)
			log(name + " left (You were kicked from this server.)")
			log(name + " was kicked.")
		case "ban": // "ban <id> [name]"
			id, _, _ := strings.Cut(arg, " ")
			if banned[id] {
				log(id + " is already banned.")
				break
			}
			banned[id] = true
			label := id
			if name, on := players[id]; on {
				label = name
				delete(players, id)
				log(name + " left (You were banned from this server.)")
			}
			log(label + " was banned.")
		case "unban":
			if !banned[arg] {
				log("That SteamID64 is not banned.")
				break
			}
			delete(banned, arg)
			log(arg + " was unbanned.")
		case "teleport": // "teleport <id|id,id,...> <player|x y z>"
			movers, where, _ := strings.Cut(arg, " ")
			sent := 0
			for _, id := range strings.Split(movers, ",") {
				if _, on := players[id]; on {
					sent++
				}
			}
			if sent == 0 {
				log("Nobody to teleport.")
				break
			}
			if name, on := players[where]; on {
				where = name
			} else {
				where = "(" + strings.ReplaceAll(where, " ", ", ") + ")"
			}
			log(fmt.Sprintf("[teleport] %d player(s) to %s by console", sent, where))
			log(fmt.Sprintf("%d player(s) teleported to %s.", sent, where))
		case "players":
			text := fmt.Sprintf("%d players", len(players))
			for id, name := range players {
				text += "\n  " + id + "  " + name
				if role := given(id); role != "" {
					text += "  (" + role + ")"
				}
			}
			log(text)
		default:
			log("Unknown command \"" + verb + "\". Type help.")
		}
	}
}
