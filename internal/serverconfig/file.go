package serverconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// File is ReSkateServer.json as a generic tree, so keys the manager does not
// know (added by a newer server) survive a write.
type File struct {
	Root map[string]any
}

// maxConfig caps how much of a ReSkateServer.json is read. The server's own
// is a few KB; one from a restore or an import could be anything.
const maxConfig = 16 << 20

// Read reads a ReSkateServer.json, which must hold one JSON object.
func Read(path string) (*File, error) {
	data, err := readCapped(path, maxConfig)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("ReSkateServer.json is not valid JSON: %w", err)
	}
	if root == nil {
		return nil, errors.New("ReSkateServer.json holds null, not settings")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("ReSkateServer.json is not valid JSON: something follows its settings")
	}
	return &File{Root: root}, nil
}

// readCapped reads a file of at most max bytes, refusing a bigger one
// rather than reading it into memory.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is over %d MB", filepath.Base(path), max>>20)
	}
	return data, nil
}

// Bytes is the file as Write writes it.
func (f *File) Bytes() ([]byte, error) {
	data, err := json.MarshalIndent(f.Root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Write replaces the file in one step, as the server does: written beside
// it, flushed to disk, then renamed over it, so a crash can't leave the
// server a config it refuses to start with. It holds the server's password
// and Steam token, so it's for the manager's user, who runs the server too.
func (f *File) Write(path string) error {
	data, err := f.Bytes()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ReSkateServer-*.json.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone already, once renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Get reads a setting by its key, dotted for nested ones ("distances.half_rate_start").
func (f *File) Get(key string) (any, bool) {
	var cur any = f.Root
	for part := range strings.SplitSeq(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Set writes a setting by its key, dotted for nested ones.
func (f *File) Set(key string, v any) {
	parts := strings.Split(key, ".")
	m := f.Root
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[part] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

func (f *File) del(key string) {
	parts := strings.Split(key, ".")
	m := f.Root
	for _, part := range parts[:len(parts)-1] {
		next, ok := m[part].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	delete(m, parts[len(parts)-1])
}

// Redact drops the owner-only settings from a copy for someone who isn't an
// owner.
func (f *File) Redact() { f.KeepOwnerOnly(nil) }

// KeepOwnerOnly puts back old's owner-only settings, dropping those old
// lacks (or all of them, with no old), so a file someone who isn't an owner
// brings in can't change them.
func (f *File) KeepOwnerOnly(old *File) {
	for _, fd := range Fields {
		if !fd.Owner {
			continue
		}
		if old != nil {
			if v, ok := old.Get(fd.Key); ok {
				f.Set(fd.Key, v)
				continue
			}
		}
		f.del(fd.Key)
	}
}

// Values returns every schema field's current value, normalised to the
// field's type (string, float64, bool, []string). Missing keys take the
// server's default, which is what it will write on its next start.
func (f *File) Values() map[string]any {
	out := map[string]any{}
	for _, fd := range Fields {
		raw, ok := f.Get(fd.Key)
		if !ok {
			out[fd.Key] = fd.Default
			continue
		}
		if v, err := normalize(fd, raw); err == nil {
			out[fd.Key] = v
		}
	}
	return out
}

// Ban is one entry in the file's bans. Admins and bans live in the same file
// as the settings but have their own pages.
type Ban struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Added int64  `json:"added"`
}

// Admins lists the in-game admins, by SteamID64.
func (f *File) Admins() []string {
	out := []string{}
	list, _ := f.Root["admins"].([]any)
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

// Bans lists the bans.
func (f *File) Bans() []Ban {
	out := []Ban{}
	list, _ := f.Root["bans"].([]any)
	for _, v := range list {
		row, ok := v.(map[string]any)
		if !ok {
			continue
		}
		b := Ban{ID: fmt.Sprint(row["id"]), Name: fmt.Sprint(row["name"])}
		if n, ok := row["added"].(json.Number); ok {
			b.Added, _ = n.Int64()
		}
		if row["name"] == nil {
			b.Name = ""
		}
		out = append(out, b)
	}
	return out
}

// SetAdmins replaces the in-game admins.
func (f *File) SetAdmins(ids []string) {
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	f.Root["admins"] = list
}

// SetBans replaces the bans.
func (f *File) SetBans(bans []Ban) {
	list := make([]any, len(bans))
	for i, b := range bans {
		list[i] = map[string]any{"id": b.ID, "name": b.Name, "added": b.Added}
	}
	f.Root["bans"] = list
}

// ---- values ----

// isControl reports whether r is a character the server refuses in chat: C0,
// DEL or C1.
func isControl(r rune) bool { return r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f }

// ValidChatText is the server's valid_chat_text (protocol.cpp): one chat
// line of at most 200 bytes of UTF-8, with no control characters, and more
// than spaces.
func ValidChatText(s string) bool {
	if s == "" || len(s) > 200 || !utf8.ValidString(s) {
		return false
	}
	visible := false
	for _, r := range s {
		if isControl(r) {
			return false
		}
		visible = visible || r != ' '
	}
	return visible
}

var fingerprintRe = regexp.MustCompile(`^[0-9a-fA-F]{1,16}$`)

// steamTokenRe is what config_error accepts: letters and digits, or empty.
var steamTokenRe = regexp.MustCompile(`^[A-Za-z0-9]*$`)

// SteamIDRe matches a player's SteamID64.
var SteamIDRe = regexp.MustCompile(`^7656119\d{10}$`)

// ServerNameRule is how the server words the name rule (server_name_rule in
// Engine/Game/Multiplayer/session_model.h).
const ServerNameRule = "1 to 64 letters, numbers, spaces and - _ / [ ] ( )"

const defaultServerName = "ReSkate server"

func nameWord(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

func nameMark(c byte) bool { return strings.IndexByte(" -_/[]()", c) >= 0 }

// ValidServerName mirrors valid_server_name: a letter or digit among the
// allowed characters and no space at either end. The server refuses to start
// with any other name, and the server browser hides one.
func ValidServerName(s string) bool {
	if s == "" || len(s) > 64 || s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	named := false
	for i := 0; i < len(s); i++ {
		if !nameWord(s[i]) && !nameMark(s[i]) {
			return false
		}
		named = named || nameWord(s[i])
	}
	return named
}

// ServerName makes a panel name into one the server accepts: other characters
// dropped, runs of spaces made one, cut to 64 bytes. With nothing left it is
// the server's default name.
func ServerName(s string) string {
	kept := []byte{}
	for i := 0; i < len(s); i++ {
		if nameWord(s[i]) || nameMark(s[i]) {
			kept = append(kept, s[i])
		}
	}
	name := strings.Join(strings.Fields(string(kept)), " ")
	if len(name) > 64 {
		name = strings.TrimRight(name[:64], " ")
	}
	if !ValidServerName(name) {
		return defaultServerName
	}
	return name
}

func normalize(fd Field, raw any) (any, error) {
	switch fd.Type {
	case TypeBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("%s must be true or false", fd.Label)
		}
		return b, nil
	case TypeInt, TypeNumber:
		var n float64
		switch v := raw.(type) {
		case json.Number:
			x, err := v.Float64()
			if err != nil {
				return nil, err
			}
			n = x
		case float64:
			n = v
		case string:
			x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be a number", fd.Label)
			}
			n = x
		default:
			return nil, fmt.Errorf("%s must be a number", fd.Label)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%s must be a number", fd.Label)
		}
		if fd.Type == TypeInt && n != math.Trunc(n) {
			return nil, fmt.Errorf("%s must be a whole number", fd.Label)
		}
		if n < fd.Min || (fd.Max != 0 && n > fd.Max) {
			return nil, fmt.Errorf("%s must be %g to %g", fd.Label, fd.Min, fd.Max)
		}
		if fd.Key == "crowd_budget" && n != 0 && n < 300 {
			return nil, fmt.Errorf("%s must be 0 (no limit) or 300 to %g", fd.Label, fd.Max)
		}
		if fd.Key == "bone_scale_limit" && n != 0 && n < 1 {
			return nil, fmt.Errorf("%s must be 0 (no limit) or 1 to %g", fd.Label, fd.Max)
		}
		return n, nil
	case TypeEnum:
		s := fmt.Sprint(raw)
		if n, ok := raw.(float64); ok {
			s = strconv.FormatFloat(n, 'f', -1, 64)
		}
		if fd.Key == "object_placement" && s == "host" {
			s = "admins"
		}
		if !slices.Contains(fd.Options, s) {
			return nil, fmt.Errorf("%s must be one of %s", fd.Label, strings.Join(fd.Options, ", "))
		}
		return s, nil
	case TypeString:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be text", fd.Label)
		}
		if fd.Key == "steam_token" {
			s = strings.TrimSpace(s) // a pasted token often brings a space or tab along
		}
		if strings.ContainsAny(s, "\r\n") {
			return nil, fmt.Errorf("%s must be one line", fd.Label)
		}
		// The server takes changes on its console, where one ends a line or,
		// on Windows, its input for good.
		if !utf8.ValidString(s) || strings.ContainsFunc(s, isControl) {
			return nil, fmt.Errorf("%s can't hold control characters", fd.Label)
		}
		if fd.MaxLen > 0 && len(s) > fd.MaxLen {
			return nil, fmt.Errorf("%s must be at most %d bytes", fd.Label, fd.MaxLen)
		}
		if fd.Key == "welcome" && s != "" && !ValidChatText(s) {
			return nil, fmt.Errorf("%s must be one chat line, not only spaces", fd.Label)
		}
		if fd.Key == "name" {
			if s = strings.TrimSpace(s); !ValidServerName(s) {
				return nil, fmt.Errorf("%s must be %s", fd.Label, ServerNameRule)
			}
		}
		if fd.Key == "map" && strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("%s cannot be empty", fd.Label)
		}
		if fd.Key == "steam_token" && !steamTokenRe.MatchString(s) {
			return nil, fmt.Errorf("%s must be letters and digits only, or empty", fd.Label)
		}
		return s, nil
	case TypeList:
		var items []string
		switch v := raw.(type) {
		case []any:
			for _, x := range v {
				items = append(items, fmt.Sprint(x))
			}
		case []string:
			items = v
		default:
			return nil, fmt.Errorf("%s must be a list", fd.Label)
		}
		out := []string{}
		for _, it := range items {
			it = strings.ToLower(strings.TrimSpace(it))
			if fd.Key == "score_allow" && !fingerprintRe.MatchString(it) {
				return nil, fmt.Errorf("%q is not a scoring fingerprint (16 hex digits)", it)
			}
			if fd.Key == "reserved" && !SteamIDRe.MatchString(it) {
				return nil, fmt.Errorf("%q is not a SteamID64 (17 digits starting 7656119)", it)
			}
			if !slices.Contains(out, it) {
				out = append(out, it)
			}
		}
		if fd.Key == "reserved" && len(out) > 1024 {
			return nil, fmt.Errorf("%s holds at most 1024 players", fd.Label)
		}
		return out, nil
	case TypeMaps:
		var items []any
		switch v := raw.(type) {
		case []any:
			items = v
		case []string:
			for _, s := range v {
				items = append(items, s)
			}
		default:
			return nil, fmt.Errorf("%s must be a list", fd.Label)
		}
		// Like load_config, skip what isn't a map name; the server matches names
		// without case, so a repeat is dropped.
		out := []string{}
		for _, x := range items {
			s, ok := x.(string)
			if s = strings.TrimSpace(s); !ok || s == "" {
				continue
			}
			if strings.ContainsAny(s, "\r\n") {
				return nil, fmt.Errorf("%s: map names are one line", fd.Label)
			}
			if !slices.ContainsFunc(out, func(m string) bool { return strings.EqualFold(m, s) }) {
				out = append(out, s)
			}
		}
		return out, nil
	}
	return nil, errors.New("unknown field type")
}

// jsonValue is how a normalised value is stored in the file.
func jsonValue(fd Field, v any) any {
	switch fd.Type {
	case TypeInt:
		return json.Number(strconv.FormatInt(int64(v.(float64)), 10))
	case TypeNumber:
		return json.Number(strconv.FormatFloat(v.(float64), 'f', -1, 64))
	case TypeList, TypeMaps:
		list := []any{}
		for _, s := range v.([]string) {
			list = append(list, s)
		}
		return list
	}
	return v
}

func num(v any) string { return strconv.FormatFloat(v.(float64), 'f', -1, 64) }

func onOff(v any) string {
	if v.(bool) {
		return "on"
	}
	return "off"
}

// Plan is what applying a set of changes takes.
type Plan struct {
	Commands []string       `json:"commands"` // run on a live server, in order
	Restart  map[string]any `json:"restart"`  // written to the file while the server is stopped
	Changed  map[string]any `json:"changed"`  // every changed key with its new value
}

// Diff validates the requested values against the current file and plans
// the commands. Unchanged keys are ignored. known is the maps the server
// knows, in its order: the running server's, or what it loads at its next
// start. A changed map pool must name only those, since the server refuses
// to start with any other, and planning one live may need the whole list.
func Diff(cur *File, want map[string]any, known []string) (*Plan, error) {
	curVals := cur.Values()
	p := &Plan{Restart: map[string]any{}, Changed: map[string]any{}}
	for key, raw := range want {
		fd, ok := FieldByKey(key)
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", key)
		}
		v, err := normalize(fd, raw)
		if err != nil {
			return nil, err
		}
		if equal(curVals[key], v) {
			continue
		}
		if fd.Type == TypeMaps {
			if v, err = knownMaps(v.([]string), known); err != nil {
				return nil, err
			}
			if equal(curVals[key], v) {
				continue
			}
		}
		p.Changed[key] = v
	}
	merged := map[string]any{}
	maps.Copy(merged, curVals)
	maps.Copy(merged, p.Changed)
	if err := validate(merged); err != nil {
		return nil, err
	}

	distances := false
	// The running server takes reserved slots only below its own max players,
	// so with a new max they wait for the restart too.
	_, newMax := p.Changed["max_players"]
	for _, fd := range Fields { // schema order gives a stable command order
		v, ok := p.Changed[fd.Key]
		if !ok {
			continue
		}
		if fd.Restart || (fd.Key == "reserved_slots" && newMax) {
			p.Restart[fd.Key] = v
			continue
		}
		switch fd.Key {
		case "name":
			p.Commands = append(p.Commands, "name "+v.(string))
		case "map":
			p.Commands = append(p.Commands, "map "+v.(string))
		case "map_pool":
			old, _ := curVals[fd.Key].([]string)
			cmds, err := poolCommands(old, v.([]string), known)
			if err != nil {
				return nil, err
			}
			p.Commands = append(p.Commands, cmds...)
		case "map_rotation_minutes":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "rotation off")
			} else {
				p.Commands = append(p.Commands, "rotation "+num(v))
			}
		case "password", "welcome":
			s := v.(string)
			if s == "" {
				s = "off"
			} else if s == "off" {
				return nil, fmt.Errorf("%s cannot be the word \"off\"", fd.Label)
			}
			p.Commands = append(p.Commands, fd.Key+" "+s)
		case "listed":
			p.Commands = append(p.Commands, "listed "+onOff(v))
		case "reserved_slots":
			p.Commands = append(p.Commands, "reserved slots "+num(v))
		case "reserved":
			old, _ := curVals[fd.Key].([]string)
			next := v.([]string)
			for _, id := range old {
				if !slices.Contains(next, id) {
					p.Commands = append(p.Commands, "reserved remove "+id)
				}
			}
			for _, id := range next {
				if !slices.Contains(old, id) {
					p.Commands = append(p.Commands, "reserved add "+id)
				}
			}
		case "send_rate":
			p.Commands = append(p.Commands, "rate "+num(v))
		case "crowd_budget":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "crowd off")
			} else {
				p.Commands = append(p.Commands, "crowd "+num(v))
			}
		case "distances.full_rate_return", "distances.half_rate_start", "distances.half_rate_return", "distances.low_rate_start":
			if !distances {
				distances = true
				p.Commands = append(p.Commands, fmt.Sprintf("distances %s %s %s %s",
					num(merged["distances.full_rate_return"]), num(merged["distances.half_rate_start"]),
					num(merged["distances.half_rate_return"]), num(merged["distances.low_rate_start"])))
			}
		case "voice_chat":
			p.Commands = append(p.Commands, "voice "+onOff(v))
		case "voice_range":
			p.Commands = append(p.Commands, "voice-range "+num(v))
		case "object_placement":
			p.Commands = append(p.Commands, "placement "+v.(string))
		case "object_limit":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "objects off")
			} else {
				p.Commands = append(p.Commands, "objects "+num(v))
			}
		case "bone_scale_limit":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "bone-scale off")
			} else {
				p.Commands = append(p.Commands, "bone-scale "+num(v))
			}
		case "noclip":
			p.Commands = append(p.Commands, "noclip "+onOff(v))
		case "no_bail":
			p.Commands = append(p.Commands, "nobail "+onOff(v))
		case "boosts":
			p.Commands = append(p.Commands, "boosts "+onOff(v))
		case "enforce_tuning":
			p.Commands = append(p.Commands, "tuning "+onOff(v))
		case "parties":
			p.Commands = append(p.Commands, "parties "+onOff(v))
		case "party_size":
			p.Commands = append(p.Commands, "party-size "+num(v))
		case "announce_throwdowns":
			p.Commands = append(p.Commands, "announce-throwdowns "+onOff(v))
		case "activity_log":
			p.Commands = append(p.Commands, "activity-log "+onOff(v))
		case "parks.construction", "parks.historic", "parks.financial":
			p.Commands = append(p.Commands, "park "+strings.TrimPrefix(fd.Key, "parks.")+" "+v.(string))
		case "votes.map.enabled", "votes.kick.enabled", "votes.time_of_day.enabled":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" "+onOff(v))
		case "votes.map.percent", "votes.kick.percent", "votes.time_of_day.percent":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" "+num(v))
		case "votes.seconds":
			p.Commands = append(p.Commands, "votes seconds "+num(v))
		case "votes.cooldown_seconds":
			p.Commands = append(p.Commands, "votes cooldown "+num(v))
		case "speed_check":
			p.Commands = append(p.Commands, "speed-check "+v.(string))
		case "score_check":
			p.Commands = append(p.Commands, "score-check "+v.(string))
		case "score_allow":
			old, _ := curVals[fd.Key].([]string)
			next := v.([]string)
			for _, fp := range old {
				if !slices.Contains(next, fp) {
					p.Commands = append(p.Commands, "score-allow remove "+fp)
				}
			}
			for _, fp := range next {
				if !slices.Contains(old, fp) {
					p.Commands = append(p.Commands, "score-allow "+fp)
				}
			}
		case "world_layer_sync":
			p.Commands = append(p.Commands, "layer-sync "+onOff(v))
		default:
			return nil, fmt.Errorf("no command for %s", fd.Key)
		}
	}
	return p, nil
}

// SettingVerbs are the console commands that change settings: every one Diff
// plans, plus the world page's tod, layers and layer, tps, which servers
// before 1.1.5 still take, and the in-game menu's names the server takes for
// some of them. Typed into the console, they need the same permission as the
// settings pages.
var SettingVerbs = []string{"name", "map", "map-pool", "rotation", "password", "welcome", "listed", "reserved", "rate", "crowd", "tps", "distances",
	"voice", "voice-range", "placement", "objects", "object-limit", "bone-scale", "noclip", "nobail", "boosts", "tuning", "parties", "party-size",
	"announce-throwdowns", "activity-log", "park", "votes", "speed-check", "score-check", "score-allow", "layer-sync", "tod", "layers", "layer",
	"voice-allow", "object-placement", "world-layer-sync", "noclip-allow", "nobail-allow", "boosts-allow", "tuning-enforce"}

// knownMaps checks a map pool against the server's maps and spells each as
// the server does, so the file it saves compares equal.
func knownMaps(pool, known []string) ([]string, error) {
	out := make([]string, 0, len(pool))
	for _, m := range pool {
		i := slices.IndexFunc(known, func(k string) bool { return strings.EqualFold(k, m) })
		if i < 0 {
			return nil, fmt.Errorf("%q is not one of the server's maps", m)
		}
		out = append(out, known[i])
	}
	return out, nil
}

// poolCommands plans the map-pool commands that turn the pool from cur into
// want (Host::command in Server/server_host.cpp). The server only appends a
// map, removes one (never the last) or clears the pool, so the maps kept are
// the longest start of want that cur already holds in order, and the rest
// of want is added after them. all is the server's maps in its order: an
// empty pool means all of them, and add refuses those.
func poolCommands(cur, want, all []string) ([]string, error) {
	in := func(list []string, m string) bool {
		return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, m) })
	}
	if len(want) == 0 {
		return []string{"map-pool clear"}, nil
	}
	var cmds []string
	if len(cur) == 0 {
		// Removing a map from every map fills the pool with all the others.
		if len(all) == 0 {
			return nil, errors.New("the server's maps are unknown, so the map pool can't be changed")
		}
		drop := all[len(all)-1]
		if i := slices.IndexFunc(all, func(m string) bool { return !in(want, m) }); i >= 0 {
			drop = all[i]
		}
		cmds = append(cmds, "map-pool remove "+drop)
		cur = slices.DeleteFunc(slices.Clone(all), func(m string) bool { return strings.EqualFold(m, drop) })
	}
	kept := 0
	for _, m := range cur {
		if kept < len(want) && strings.EqualFold(m, want[kept]) {
			kept++
		}
	}
	add := want[kept:]
	if kept == 0 {
		// Nothing stays, so add the first map (cur can't have it) before the
		// removes, which would otherwise empty the pool.
		cmds = append(cmds, "map-pool add "+want[0])
		add = want[1:]
	}
	for _, m := range cur {
		if !in(want[:kept], m) {
			cmds = append(cmds, "map-pool remove "+m)
		}
	}
	for _, m := range add {
		cmds = append(cmds, "map-pool add "+m)
	}
	return cmds, nil
}

func voteKind(key string) string {
	switch {
	case strings.HasPrefix(key, "votes.map."):
		return "map"
	case strings.HasPrefix(key, "votes.kick."):
		return "kick"
	}
	return "tod"
}

// ApplyOffline writes every changed value to the file (for a stopped server).
func (f *File) ApplyOffline(changed map[string]any) {
	for key, v := range changed {
		fd, _ := FieldByKey(key)
		f.Set(key, jsonValue(fd, v))
	}
}

func equal(a, b any) bool {
	as, aok := a.([]string)
	bs, bok := b.([]string)
	if aok || bok {
		return aok && bok && slices.Equal(as, bs)
	}
	return a == b
}

// validate mirrors the cross-field checks in config_error().
func validate(v map[string]any) error {
	f := func(k string) float64 { x, _ := v[k].(float64); return x }
	full, half, halfRet, low := f("distances.full_rate_return"), f("distances.half_rate_start"), f("distances.half_rate_return"), f("distances.low_rate_start")
	if !(full < half && half <= halfRet && halfRet < low && low <= 10000) {
		return errors.New("distances must be ordered: full rate return < half rate start <= half rate return < low rate start <= 10000")
	}
	if f("port") == f("query_port") {
		return errors.New("the game port and query port must differ")
	}
	if f("reserved_slots") >= f("max_players") {
		return errors.New("reserved slots must be fewer than max players, so that anyone can join at all")
	}
	return nil
}
