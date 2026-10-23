// Package parks keeps what each server's Parks page sets that the server
// itself has no setting for, and acts on it: a pool of parks that goes up as
// its map loads, and who may spawn and remove parks from chat with /parks.
package parks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xThrasherrr/ReSkateManager/internal/instance"
	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/store"
)

// Word is the chat command players spawn and remove parks with.
const Word = "parks"

// Source is the plugin /parks names among the server's chat commands
// (serverconfig.PluginCommand): the manager's own, which no plugin can be,
// as a plugin's ID has no ':'.
const Source = "manager:parks"

// Pool is parks that go up together on Map, ones known not to overlap. As
// Map loads, the manager spawns one of its pools at random.
type Pool struct {
	Name  string   `json:"name"`
	Map   string   `json:"map"`
	Parks []string `json:"parks"`
}

// Chat is who may use /parks: everyone, or players whose role is in Roles.
// A park they spawn goes after Minutes, or stays until removed with 0.
type Chat struct {
	Everyone bool     `json:"everyone"`
	Roles    []string `json:"roles"`
	Minutes  int      `json:"minutes"`
}

// On reports whether anyone may use /parks.
func (c Chat) On() bool { return c.Everyone || len(c.Roles) > 0 }

// Settings are one server's.
type Settings struct {
	Pools []Pool `json:"pools"`
	Chat  Chat   `json:"chat"`
}

const (
	maxPools   = 32
	maxPoolLen = 32 // a pool's name, in bytes
	maxRoles   = 32 // as the server has at most
	maxMinutes = 1440
)

// key is where a server's settings are kept; store.DeleteInstance deletes them with it.
func key(id string) string { return "parks:" + id }

// Load reads a server's settings.
func Load(ctx context.Context, s *store.Store, id string) (Settings, error) {
	return store.LoadJSON(ctx, s, key(id), Settings{Pools: []Pool{}, Chat: Chat{Roles: []string{}, Minutes: 30}})
}

// Save keeps a server's settings, checked first against the parks it has set
// up and how many it may spawn at once.
func Save(ctx context.Context, s *store.Store, id string, set Settings, setup []serverconfig.ParkMod, limit int) error {
	if err := check(set, setup, limit); err != nil {
		return err
	}
	return store.SaveJSON(ctx, s, key(id), set)
}

func check(set Settings, setup []serverconfig.ParkMod, limit int) error {
	if len(set.Pools) > maxPools {
		return fmt.Errorf("at most %d pools", maxPools)
	}
	for i := range set.Pools {
		p := &set.Pools[i]
		if p.Name = strings.TrimSpace(p.Name); p.Name == "" || len(p.Name) > maxPoolLen || !utf8.ValidString(p.Name) || strings.ContainsFunc(p.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return fmt.Errorf("a pool's name is 1 to %d characters", maxPoolLen)
		}
		if slices.ContainsFunc(set.Pools[:i], func(o Pool) bool { return strings.EqualFold(o.Name, p.Name) }) {
			return fmt.Errorf("two pools are called %s", p.Name)
		}
		if len(p.Parks) == 0 {
			return fmt.Errorf("pool %s has no parks", p.Name)
		}
		if len(p.Parks) > limit {
			return fmt.Errorf("pool %s has %d parks, and the server spawns at most %d at once", p.Name, len(p.Parks), limit)
		}
		for j, name := range p.Parks {
			at := slices.IndexFunc(setup, func(pm serverconfig.ParkMod) bool { return pm.Name == name })
			switch {
			case at < 0:
				return fmt.Errorf("pool %s: %q isn't set up", p.Name, name)
			case !strings.EqualFold(setup[at].Map, p.Map):
				return fmt.Errorf("pool %s is for %s, and %s is for %s", p.Name, p.Map, name, setup[at].Map)
			case slices.Contains(p.Parks[:j], name):
				return fmt.Errorf("pool %s has %s twice", p.Name, name)
			}
		}
	}
	if set.Chat.Minutes < 0 || set.Chat.Minutes > maxMinutes {
		return fmt.Errorf("a park spawned from chat stays 1 to %d minutes, or 0 until removed", maxMinutes)
	}
	if len(set.Chat.Roles) > maxRoles {
		return fmt.Errorf("at most %d roles", maxRoles)
	}
	for _, r := range set.Chat.Roles {
		if !serverconfig.RoleNameRe.MatchString(r) {
			return fmt.Errorf("%q isn't a role's name", r)
		}
	}
	return nil
}

// Service acts on the servers' settings.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	gaps  gaps
}

// Words is the chat command a server needs, for PluginCommands: none while
// nobody may use /parks.
func (s *Service) Words(id string) []serverconfig.Word {
	set, err := Load(context.Background(), s.Store, id)
	if err != nil {
		s.Log.Warn("park settings", "instance", id, "err", err)
		return nil
	}
	if !set.Chat.On() {
		return nil
	}
	return []serverconfig.Word{{Name: Word, Plugin: Source}}
}

// Notice is the hook for a server's OnNotice: it spawns a pool as a map
// loads, and answers /parks. Notices come under the server's lock, so what
// it does waits for nothing here.
func (s *Service) Notice(in *instance.Instance) func(instance.Notice) {
	return func(n instance.Notice) {
		switch {
		case n.Kind == instance.NoticeMap, n.Kind == instance.NoticeState && n.State == instance.Running:
			if n.Map != "" {
				go s.spawnPool(in, n.Map)
			}
		case n.Kind == instance.NoticeCommand && n.Plugin == Source && n.Command == Word:
			go s.command(in, n.Player, n.Text)
		}
	}
}

// spawnPool spawns one of the pools for the map that loaded, at random, all
// of its parks in one park-mod add, which spawns them all or none.
func (s *Service) spawnPool(in *instance.Instance, mapName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	set, err := Load(ctx, s.Store, in.ID())
	if err != nil {
		s.Log.Warn("park settings", "instance", in.ID(), "err", err)
		return
	}
	pools := slices.DeleteFunc(slices.Clone(set.Pools), func(p Pool) bool { return !strings.EqualFold(p.Map, mapName) })
	if len(pools) == 0 {
		return
	}
	p := pools[rand.N(len(pools))]
	// A park taken out of the setup since is left out, or none would spawn.
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		in.Note("Couldn't spawn the park pool " + p.Name + ": " + err.Error())
		return
	}
	setup, _ := f.Values()["park_mods"].([]serverconfig.ParkMod)
	names := slices.DeleteFunc(slices.Clone(p.Parks), func(name string) bool {
		return !slices.ContainsFunc(setup, func(pm serverconfig.ParkMod) bool { return pm.Name == name && strings.EqualFold(pm.Map, mapName) })
	})
	if len(names) == 0 {
		return
	}
	if _, err := in.Command(ctx, "park-mod add "+strings.Join(names, " "), "pool "+p.Name); err != nil {
		in.Note("Couldn't spawn the park pool " + p.Name + ": " + err.Error())
	}
}

// commandGap keeps one player's /parks apart, so nobody can flood the server
// with park-mod.
const commandGap = 3 * time.Second

var steamID = regexp.MustCompile(`^7656119\d{10}$`)

// command answers a player's /parks [list] | add <name> | remove <name>.
func (s *Service) command(in *instance.Instance, p instance.Player, text string) {
	// No ID: no telling who, nor answering them. It goes into msg, so only digits.
	if !steamID.MatchString(p.ID) || !s.gaps.ok(in.ID()+" "+p.ID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	by := p.Name + " (/parks)"
	tell := func(msg string) {
		if _, err := in.Command(ctx, "msg "+p.ID+" "+msg, by); err != nil {
			s.Log.Warn("/parks reply", "instance", in.ID(), "err", err)
		}
	}
	set, err := Load(ctx, s.Store, in.ID())
	f, ferr := serverconfig.Read(in.Def().ConfigPath())
	if err = errors.Join(err, ferr); err != nil {
		s.Log.Warn("/parks", "instance", in.ID(), "err", err)
		tell("Parks can't be spawned right now.")
		return
	}
	if !allowed(set.Chat, f, p.ID) {
		tell("Your role can't spawn parks here.")
		return
	}
	setup, _ := f.Values()["park_mods"].([]serverconfig.ParkMod)
	verb, name, _ := strings.Cut(strings.ToLower(strings.TrimSpace(text)), " ")
	name = strings.TrimSpace(name)
	usage := "Type /parks add <name> or /parks remove <name>; /parks lists them."
	switch verb {
	case "", "list":
		tell(listing(ctx, in, setup) + " " + usage)
	case "add", "remove":
		// Only a park that's set up goes into the command: nothing else a
		// player typed reaches the console.
		if !serverconfig.ValidParkName(name) || !slices.ContainsFunc(setup, func(pm serverconfig.ParkMod) bool { return pm.Name == name }) {
			tell("No park is called that. " + usage)
			return
		}
		cmd := "park-mod " + verb + " " + name
		if verb == "add" && set.Chat.Minutes > 0 {
			cmd += " " + strconv.Itoa(set.Chat.Minutes)
		}
		reply, err := in.Command(ctx, cmd, by)
		if err != nil {
			tell("That didn't work: " + err.Error())
			return
		}
		// The first line only: a msg is one line, and the rest is the log's.
		first, _, _ := strings.Cut(reply, "\n")
		tell(clip(strings.TrimSuffix(strings.TrimPrefix(first, "[parks] "), " by console"), 180))
	default:
		tell(usage)
	}
}

// allowed reports whether the player may use /parks: anyone, a player whose
// role is listed, or one whose role may run park-mod itself.
func allowed(c Chat, f *serverconfig.File, id string) bool {
	if c.Everyone {
		return true
	}
	roles, given, def := f.Roles()
	role := given[id]
	if role == "" {
		role = def
	}
	if role == "" {
		return false
	}
	if slices.Contains(c.Roles, role) {
		return true
	}
	at := slices.IndexFunc(roles, func(r serverconfig.Role) bool { return r.Name == role })
	return at >= 0 && (slices.Contains(roles[at].Commands, "*") || slices.Contains(roles[at].Commands, "park-mod"))
}

// listing is the parks set up for the map that runs, marking the ones up.
func listing(ctx context.Context, in *instance.Instance, setup []serverconfig.ParkMod) string {
	here := in.View().Info.Map
	up := map[string]bool{}
	if spawned, err := in.SpawnedParks(ctx); err == nil {
		for _, sp := range spawned {
			up[sp.Name] = true
		}
	}
	var names []string
	for _, pm := range setup {
		if strings.EqualFold(pm.Map, here) {
			if up[pm.Name] {
				names = append(names, pm.Name+" (up)")
			} else {
				names = append(names, pm.Name)
			}
		}
	}
	if len(names) == 0 {
		return "No parks are set up for this map."
	}
	// With the usage after it, one chat line.
	return clip("Parks here: "+strings.Join(names, ", ")+".", 120)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// gaps is when each player last used /parks.
type gaps struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (g *gaps) ok(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if t, ok := g.last[key]; ok && now.Sub(t) < commandGap {
		return false
	}
	if len(g.last) >= 1024 || g.last == nil { // the old ones are long over
		g.last = map[string]time.Time{}
	}
	g.last[key] = now
	return true
}
