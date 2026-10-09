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
	"strings"
	"time"
)

func log(text string) { fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), text) }

// slowAdmin is made an admin at once, but the reply comes a second later.
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
	log("Fake is up on San Vansterdam for 16 players.")
	log("Steam ID 90000000000000001, public IP 1.2.3.4.")
	log("Join code: CODE-1")
	log("1 admin(s). Type help for commands.")
	players := map[string]string{}
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
			log(fmt.Sprintf("%s joined (%s), %d/16 players, loaded in 3 s", name, id, len(players)))
			log("ok")
		case "leave":
			name := players[arg]
			delete(players, arg)
			log(name + " left (Disconnected)")
			log("ok")
		case "admin": // "admin add|remove <id>", saved to ReSkateServer.json like the real server
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
		case "say":
			log("[chat] Server: " + arg)
		case "announce": // 2.0.2; test hook: a file "before-2.0.2" beside it makes it older
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "before-2.0.2")); err == nil {
				log("Unknown command \"" + verb + "\". Type help.")
				break
			}
			log("[announcement] " + arg)
			log("Announced.")
		case "players":
			text := fmt.Sprintf("%d players", len(players))
			for id, name := range players {
				text += "\n  " + id + "  " + name
			}
			log(text)
		default:
			log("Unknown command \"" + verb + "\". Type help.")
		}
	}
}
