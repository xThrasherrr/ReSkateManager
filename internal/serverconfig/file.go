package serverconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
//
// Since ReSkate 1.1.7 the server writes its settings in sections, some under
// new names, and keeps its bans in data/bans.json beside the file. It still
// reads a file from before then, flat, and writes it back in sections on its
// first start. Keys here are always the flat names, as the panel, the API and
// the console commands use them; Get and Set find each one's place in either
// layout.
type File struct {
	Root map[string]any

	bans     []Ban // data/bans.json, when the server keeps them there
	bansFile bool  // the bans belong in data/bans.json
	bansSet  bool  // SetBans changed them, so Write writes data/bans.json
}

// maxConfig caps how much of a ReSkateServer.json is read. The server's own
// is a few KB; one from a restore or an import could be anything.
const maxConfig = 16 << 20

// BansFile is where a server from ReSkate 1.1.7 on keeps its bans, relative
// to its folder.
const BansFile = "data/bans.json"

// Read reads a ReSkateServer.json, which must hold one JSON object, and the
// bans in data/bans.json beside it.
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
	f := &File{Root: root}
	data, err = readCapped(filepath.Join(filepath.Dir(path), BansFile), maxConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	// The server won't start with bans it can't read either.
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var rows []any
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("%s is not a JSON list: %w", BansFile, err)
	}
	f.bans, f.bansFile = bansOf(rows), true
	return f, nil
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
// Changed bans that belong in data/bans.json go there first, as the server
// writes them, so a config that held them loses them only once they're safe.
func (f *File) Write(path string) error {
	data, err := f.Bytes()
	if err != nil {
		return err
	}
	if f.bansSet && f.bansFile {
		rows := make([]any, len(f.bans))
		for i, b := range f.bans {
			rows[i] = map[string]any{"id": b.ID, "name": b.Name, "added": b.Added}
		}
		bans, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		bansPath := filepath.Join(filepath.Dir(path), BansFile)
		if err := os.MkdirAll(filepath.Dir(bansPath), 0o755); err != nil {
			return err
		}
		if err := writeAtomic(bansPath, append(bans, '\n')); err != nil {
			return err
		}
	}
	return writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
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

// sections says where a sectioned file keeps each flat key (load_config in
// Server/server_config.cpp, 1.1.7). A nested key goes by its first part, so
// "distances.half_rate_start" is in network.distances. Keys not listed, the
// votes and 2.0.2's announcements, are in the same place in both layouts.
var sections = map[string]string{
	"name": "server.name", "password": "server.password", "welcome": "server.welcome_message", "listed": "server.listed",
	"max_players": "server.max_players", "port": "server.port", "query_port": "server.query_port",
	"steam_token": "server.steam_token", "auto_update": "server.auto_update", "activity_log": "server.activity_log",
	"chat_color": "server.chat_color", "chat_text_color": "server.chat_text_color",

	"admins": "access.admins", "reserved": "access.reserved_players_slots", "global_bans": "access.use_global_bans",

	"map": "maps.map", "map_pool": "maps.pool", "map_rotation_minutes": "maps.rotation_minutes", "parks": "maps.parks",
	"world_layer_sync": "maps.world_layer_sync", "layers": "maps.layers",

	"boosts": "players.allow_boosts", "no_bail": "players.allow_no_bail", "noclip": "players.allow_noclip",
	"parties": "players.allow_parties", "party_size": "players.party_size", "voice_chat": "players.allow_voice_chat",
	"voice_range": "players.voice_range", "object_placement": "players.object_placement",
	"object_limit": "players.object_limit", "announce_throwdowns": "players.announce_throwdowns",
	"afk_kick_minutes": "players.afk_kick_minutes", "allow_object_scaling": "players.allow_object_scaling", "sync_effects": "players.sync_effects",

	"speed_check": "anti_cheat.speed_hack", "score_check": "anti_cheat.modified_scoring",
	"score_allow": "anti_cheat.allowed_scoring_mods", "enforce_tuning": "anti_cheat.enforce_tuning",
	"bone_scale_limit": "anti_cheat.bone_scale_limit",

	"send_rate": "network.send_rate", "crowd_budget": "network.crowd_budget", "distances": "network.distances",
	"use_steam_relay": "network.use_steam_relay", "pack_ms": "network.pack_ms", "finger_distance": "network.finger_distance",
	"steam_debug": "network.steam_debug", "threads": "network.threads",
}

// Removed are the flat settings a sectioned server no longer has: reserved
// slots are kept for each listed player instead.
var Removed = []string{"reserved_slots"}

// Sectioned reports whether the file is laid out in sections, as servers from
// ReSkate 1.1.7 on write it.
func (f *File) Sectioned() bool {
	for _, s := range []string{"server", "access", "maps", "players", "anti_cheat", "network"} {
		if _, ok := f.Root[s].(map[string]any); ok {
			return true
		}
	}
	return false
}

// places lists where the server looks for key, in its order: in its section,
// at the top under its new name, then under the flat name.
func places(key string) []string {
	head, rest, nested := strings.Cut(key, ".")
	at, ok := sections[head]
	if !ok {
		return []string{key}
	}
	_, name, _ := strings.Cut(at, ".")
	out := []string{at}
	for _, p := range []string{name, head} {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if nested {
		for i := range out {
			out[i] += "." + rest
		}
	}
	return out
}

// Get reads a setting by its flat key, dotted for nested ones
// ("distances.half_rate_start"), from wherever the server would take it.
func (f *File) Get(key string) (any, bool) {
	for _, p := range places(key) {
		if v, ok := f.at(p); ok {
			return v, true
		}
	}
	return nil, false
}

// Set writes a setting by its flat key: into its section when the file has
// them, dropping any copy left at the top, which the server would not read.
func (f *File) Set(key string, v any) {
	if !f.Sectioned() {
		f.setAt(key, v)
		return
	}
	all := places(key)
	for _, p := range all[1:] {
		f.delAt(p)
	}
	f.setAt(all[0], v)
}

func (f *File) del(key string) {
	for _, p := range places(key) {
		f.delAt(p)
	}
}

func (f *File) at(path string) (any, bool) {
	var cur any = f.Root
	for part := range strings.SplitSeq(path, ".") {
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

func (f *File) setAt(path string, v any) {
	parts := strings.Split(path, ".")
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

func (f *File) delAt(path string) {
	parts := strings.Split(path, ".")
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

// Announces reports whether the server whose config is at path has the
// announce command, which came with its announcements in ReSkate 2.0.2: a
// running server has written every setting it knows into the file.
func Announces(path string) bool {
	f, err := Read(path)
	if err != nil {
		return false
	}
	_, ok := f.Get("announcements.card")
	return ok
}

// Ban is one of the server's bans. Admins and bans have their own pages.
type Ban struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Added int64  `json:"added"`
}

// Admins lists the in-game admins, by SteamID64.
func (f *File) Admins() []string {
	out := []string{}
	v, _ := f.Get("admins")
	list, _ := v.([]any)
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

func bansOf(list []any) []Ban {
	out := []Ban{}
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

// Bans lists the bans as the server reads them: data/bans.json, then any
// still in the config, which it moves there.
func (f *File) Bans() []Ban {
	out := slices.Clone(f.bans)
	list, _ := f.Root["bans"].([]any)
	for _, b := range bansOf(list) {
		if !slices.ContainsFunc(out, func(x Ban) bool { return x.ID == b.ID }) {
			out = append(out, b)
		}
	}
	if out == nil {
		out = []Ban{}
	}
	return out
}

// SetAdmins replaces the in-game admins.
func (f *File) SetAdmins(ids []string) {
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	f.Set("admins", list)
}

// SetBans replaces the bans: in data/bans.json for a server that keeps them
// there, else in the config.
func (f *File) SetBans(bans []Ban) {
	if f.bansFile || f.Sectioned() {
		f.bans, f.bansFile, f.bansSet = slices.Clone(bans), true, true
		delete(f.Root, "bans")
		return
	}
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

var colorRe = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

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
		n, ok := asNumber(raw)
		if !ok {
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
		if ownSeconds(fd.Key) && n != 0 && n < minVoteSeconds {
			return nil, fmt.Errorf("%s must be 0 (the vote length) or %d to %g", fd.Label, minVoteSeconds, fd.Max)
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
	case TypeColor:
		// parse_colour: six hex digits, the # optional. The server won't start
		// with anything else, so it's written back the one way.
		s, ok := raw.(string)
		if s = strings.TrimPrefix(strings.TrimSpace(s), "#"); !ok || !colorRe.MatchString(s) {
			return nil, fmt.Errorf("%s must be a colour like #8E5CFF", fd.Label)
		}
		return "#" + strings.ToUpper(s), nil
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
	case TypeLines:
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
		// Like load_config, skip what isn't text. The console's add trims a
		// line, so it is kept trimmed; repeats are the owner's to keep.
		out := []string{}
		for _, x := range items {
			s, ok := x.(string)
			if s = strings.TrimSpace(s); !ok || s == "" {
				continue
			}
			if !ValidChatText(s) || fd.MaxLen > 0 && len(s) > fd.MaxLen {
				return nil, fmt.Errorf("%s: each must be one chat line of at most %d bytes (%q is not)", fd.Label, fd.MaxLen, s)
			}
			out = append(out, s)
		}
		if fd.Max > 0 && len(out) > int(fd.Max) {
			return nil, fmt.Errorf("%s holds at most %g", fd.Label, fd.Max)
		}
		return out, nil
	case TypeVotes:
		return customVotes(raw)
	}
	return nil, errors.New("unknown field type")
}

// asNumber reads a number as JSON, the panel or a hand-written file gives it.
func asNumber(raw any) (float64, bool) {
	var n float64
	switch v := raw.(type) {
	case json.Number:
		x, err := v.Float64()
		if err != nil {
			return 0, false
		}
		n = x
	case float64:
		n = v
	case string:
		x, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		n = x
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

// Limits of 2.0.2's custom votes (Server/server_config.h,
// Extension/Multiplayer/Net/protocol.h).
const (
	maxCustomVotes   = 16
	maxVoteChoices   = 8
	maxVoteNameBytes = 16
	maxVoteDescBytes = 80
	maxVoteCommand   = 320 // max_admin_text
	defaultVotePct   = 60
	minVoteSeconds   = 10
	maxVoteSeconds   = 300
	maxVoteCooldown  = 3600
	commandExample   = `e.g. "map {map}"`
)

// ownSeconds reports whether key is one vote's own length (votes.map.seconds),
// which is 0 for the votes' length, never 1 to 9.
func ownSeconds(key string) bool {
	return slices.Contains([]string{"votes.map.seconds", "votes.kick.seconds", "votes.time_of_day.seconds"}, key)
}

// validVoteName is valid_server_vote_name: 1 to 16 of a-z, 0-9, - and _.
func validVoteName(s string) bool {
	if s == "" || len(s) > maxVoteNameBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// voteNameFree is custom_vote_name_free: not a word /vote already takes, and
// not a number, which answers a poll.
func voteNameFree(s string) bool {
	if slices.Contains([]string{"map", "kick", "tod", "time", "yes", "y", "no", "n", "poll", "list"}, s) {
		return false
	}
	return strings.Trim(s, "0123456789") != ""
}

// customVotes reads votes.custom as load_config does (a vote needs no more
// than its name and command; the rest take the server's defaults) and checks
// it as custom_votes_error does, since the server won't start on one it
// refuses.
func customVotes(raw any) ([]CustomVote, error) {
	if list, ok := raw.([]CustomVote); ok {
		raw = jsonValue(Field{Type: TypeVotes}, list)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("custom votes must be a list")
	}
	if len(items) > maxCustomVotes {
		return nil, fmt.Errorf("at most %d custom votes", maxCustomVotes)
	}
	out := []CustomVote{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("each custom vote must be an object")
		}
		v := CustomVote{Choices: []string{}, Enabled: true, Percent: defaultVotePct, MinPlayers: 1}
		text := func(key string) (string, error) {
			x, ok := m[key]
			if !ok || x == nil {
				return "", nil
			}
			s, ok := x.(string)
			if !ok {
				return "", fmt.Errorf("a custom vote's %s must be text", key)
			}
			return strings.TrimSpace(s), nil
		}
		whole := func(key string, min, max int, into *int) error {
			x, ok := m[key]
			if !ok {
				return nil
			}
			n, ok := asNumber(x)
			if !ok || n != math.Trunc(n) || n < float64(min) || n > float64(max) {
				return fmt.Errorf("custom vote %q: %s must be a whole number, %d to %d", v.Name, key, min, max)
			}
			*into = int(n)
			return nil
		}
		var err error
		if v.Name, err = text("name"); err != nil {
			return nil, err
		}
		v.Name = strings.ToLower(v.Name)
		if !validVoteName(v.Name) {
			return nil, fmt.Errorf("a custom vote's name is 1 to %d lowercase letters, digits, - or _ (%q is not)", maxVoteNameBytes, v.Name)
		}
		if !voteNameFree(v.Name) {
			return nil, fmt.Errorf("custom vote %q: that name is one of the server's own votes", v.Name)
		}
		if slices.ContainsFunc(out, func(o CustomVote) bool { return o.Name == v.Name }) {
			return nil, fmt.Errorf("two custom votes are called %q", v.Name)
		}
		if v.Description, err = text("description"); err != nil {
			return nil, err
		}
		if v.Description != "" && (len(v.Description) > maxVoteDescBytes || !ValidChatText(v.Description)) {
			return nil, fmt.Errorf("custom vote %q: the description must be one chat line of at most %d bytes", v.Name, maxVoteDescBytes)
		}
		if v.Command, err = text("command"); err != nil {
			return nil, err
		}
		if v.Command == "" || len(v.Command) > maxVoteCommand || !utf8.ValidString(v.Command) || strings.ContainsFunc(v.Command, isControl) {
			return nil, fmt.Errorf("custom vote %q: the command must be one server command of at most %d bytes, %s", v.Name, maxVoteCommand, commandExample)
		}
		if x, ok := m["choices"]; ok && x != nil {
			list, ok := x.([]any)
			if !ok {
				return nil, fmt.Errorf("custom vote %q: the choices must be a list", v.Name)
			}
			for _, c := range list {
				s, _ := c.(string) // load_config skips what isn't text
				if s = strings.ToLower(strings.TrimSpace(s)); s == "" || slices.Contains(v.Choices, s) {
					continue
				}
				if !validVoteName(s) {
					return nil, fmt.Errorf("custom vote %q: each choice is 1 to %d lowercase letters, digits, - or _ (%q is not)", v.Name, maxVoteNameBytes, s)
				}
				v.Choices = append(v.Choices, s)
			}
		}
		if len(v.Choices) > maxVoteChoices {
			return nil, fmt.Errorf("custom vote %q: at most %d choices", v.Name, maxVoteChoices)
		}
		arg := strings.Contains(v.Command, "{arg}")
		if arg && len(v.Choices) == 0 {
			return nil, fmt.Errorf("custom vote %q: a command with {arg} needs choices to fill it", v.Name)
		}
		if !arg && len(v.Choices) > 0 {
			return nil, fmt.Errorf("custom vote %q: choices need {arg} in the command, where the choice goes", v.Name)
		}
		if x, ok := m["enabled"]; ok {
			if v.Enabled, ok = x.(bool); !ok {
				return nil, fmt.Errorf("custom vote %q: enabled must be true or false", v.Name)
			}
		}
		if err := whole("percent", 1, 100, &v.Percent); err != nil {
			return nil, err
		}
		if err := whole("seconds", 0, maxVoteSeconds, &v.Seconds); err != nil {
			return nil, err
		}
		if v.Seconds != 0 && v.Seconds < minVoteSeconds {
			return nil, fmt.Errorf("custom vote %q: seconds must be 0 (the votes' length) or %d to %d", v.Name, minVoteSeconds, maxVoteSeconds)
		}
		if err := whole("cooldown_seconds", 0, maxVoteCooldown, &v.Cooldown); err != nil {
			return nil, err
		}
		if err := whole("min_players", 1, maxVotePlayers, &v.MinPlayers); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// jsonValue is how a normalised value is stored in the file.
func jsonValue(fd Field, v any) any {
	switch fd.Type {
	case TypeInt:
		return json.Number(strconv.FormatInt(int64(v.(float64)), 10))
	case TypeNumber:
		return json.Number(strconv.FormatFloat(v.(float64), 'f', -1, 64))
	case TypeList, TypeMaps, TypeLines:
		list := []any{}
		for _, s := range v.([]string) {
			list = append(list, s)
		}
		return list
	case TypeVotes:
		// Every key, as the server writes them, so it adds none on its next start.
		list := []any{}
		for _, cv := range v.([]CustomVote) {
			choices := []any{}
			for _, c := range cv.Choices {
				choices = append(choices, c)
			}
			n := func(i int) json.Number { return json.Number(strconv.Itoa(i)) }
			list = append(list, map[string]any{"name": cv.Name, "description": cv.Description, "command": cv.Command,
				"choices": choices, "enabled": cv.Enabled, "percent": n(cv.Percent), "seconds": n(cv.Seconds),
				"cooldown_seconds": n(cv.Cooldown), "min_players": n(cv.MinPlayers)})
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
		if cur.Sectioned() && slices.Contains(Removed, key) {
			return nil, fmt.Errorf("the server no longer has %s: ReSkate 1.1.7 took it out", strings.ToLower(fd.Label))
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

	distances, colors, newVotes := false, false, false
	// The running server takes reserved slots only below its own max players,
	// so with a new max they wait for the restart too.
	_, newMax := p.Changed["max_players"]
	for _, fd := range Fields { // schema order gives a stable command order
		v, ok := p.Changed[fd.Key]
		if !ok {
			continue
		}
		// No command adds, removes or rewrites a custom vote, only changes
		// one's limits.
		if fd.Key == "votes.custom" {
			old, _ := curVals[fd.Key].([]CustomVote)
			newVotes = !slices.EqualFunc(old, v.([]CustomVote), sameVote)
		}
		if fd.Restart || (fd.Key == "reserved_slots" && newMax) || (fd.Key == "votes.custom" && newVotes) {
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
		case "chat_color", "chat_text_color":
			// One command sets both; the text colour can't be given alone.
			if !colors {
				colors = true
				badge, _ := merged["chat_color"].(string)
				text, _ := merged["chat_text_color"].(string)
				if badge == "" || text == "" { // the file holds one that isn't a colour
					return nil, errors.New("set both chat colours: one in the file isn't a colour")
				}
				p.Commands = append(p.Commands, "chat-color "+badge+" "+text)
			}
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
		case "allow_object_scaling":
			p.Commands = append(p.Commands, "object-scaling "+onOff(v))
		case "bone_scale_limit":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "bone-scale off")
			} else {
				p.Commands = append(p.Commands, "bone-scale "+num(v))
			}
		case "sync_effects":
			p.Commands = append(p.Commands, "effects "+onOff(v))
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
		case "afk_kick_minutes":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "afk-kick off")
			} else {
				p.Commands = append(p.Commands, "afk-kick "+num(v))
			}
		case "announce_throwdowns":
			p.Commands = append(p.Commands, "announce-throwdowns "+onOff(v))
		case "activity_log":
			p.Commands = append(p.Commands, "activity-log "+onOff(v))
		case "parks.construction", "parks.historic", "parks.financial":
			p.Commands = append(p.Commands, "park "+strings.TrimPrefix(fd.Key, "parks.")+" "+v.(string))
		case "announcements.messages":
			old, _ := curVals[fd.Key].([]string)
			p.Commands = append(p.Commands, announcementCommands(old, v.([]string))...)
		case "announcements.interval_minutes":
			if v.(float64) == 0 {
				p.Commands = append(p.Commands, "announcements interval off")
			} else {
				p.Commands = append(p.Commands, "announcements interval "+num(v))
			}
		case "announcements.card":
			p.Commands = append(p.Commands, "announcements card "+onOff(v))
		case "votes.map.enabled", "votes.kick.enabled", "votes.time_of_day.enabled":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" "+onOff(v))
		case "votes.map.percent", "votes.kick.percent", "votes.time_of_day.percent":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" "+num(v))
		case "votes.map.seconds", "votes.kick.seconds", "votes.time_of_day.seconds":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" seconds "+num(v))
		case "votes.map.cooldown_seconds", "votes.kick.cooldown_seconds", "votes.time_of_day.cooldown_seconds":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" cooldown "+num(v))
		case "votes.map.min_players", "votes.kick.min_players", "votes.time_of_day.min_players":
			p.Commands = append(p.Commands, "votes "+voteKind(fd.Key)+" min-players "+num(v))
		case "votes.seconds":
			p.Commands = append(p.Commands, "votes seconds "+num(v))
		case "votes.cooldown_seconds":
			p.Commands = append(p.Commands, "votes cooldown "+num(v))
		case "votes.starter_votes_yes":
			p.Commands = append(p.Commands, "votes starter-yes "+onOff(v))
		case "votes.custom":
			old, _ := curVals[fd.Key].([]CustomVote)
			p.Commands = append(p.Commands, customVoteCommands(old, v.([]CustomVote))...)
		case "votes.polls":
			p.Commands = append(p.Commands, "votes polls "+v.(string))
		case "votes.poll_seconds":
			p.Commands = append(p.Commands, "votes poll-seconds "+num(v))
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
// before 1.1.5 still take, and the names the server also takes for some of
// them (the in-game menu's, and chat-colour). Typed into the console, they
// need the same permission as the settings pages.
var SettingVerbs = []string{"name", "map", "map-pool", "rotation", "password", "welcome", "listed", "reserved", "rate", "crowd", "tps", "distances",
	"voice", "voice-range", "placement", "objects", "object-limit", "object-scaling", "bone-scale", "effects", "noclip", "nobail", "boosts", "tuning",
	"parties", "party-size", "afk-kick", "announce-throwdowns", "activity-log", "park", "votes", "speed-check", "score-check", "score-allow",
	"layer-sync", "tod", "layers", "layer", "chat-color", "chat-colour", "announcements",
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

// announcementCommands plans the commands that turn the announcements from
// cur into want (announcements_command in Server/server_votes.cpp). The
// server only appends one, removes one by its place or clears them, so the
// ones kept are those of cur that start want in order, the others are removed
// from the last up, which keeps the places of those before them, and the rest
// of want is added.
func announcementCommands(cur, want []string) []string {
	if len(want) == 0 {
		return []string{"announcements clear"}
	}
	kept, drop := 0, []int{}
	for i, m := range cur {
		if kept < len(want) && m == want[kept] {
			kept++
		} else {
			drop = append(drop, i)
		}
	}
	var cmds []string
	if kept == 0 && len(cur) > 0 {
		cmds = append(cmds, "announcements clear")
	} else {
		for _, i := range slices.Backward(drop) {
			cmds = append(cmds, "announcements remove "+strconv.Itoa(i+1))
		}
	}
	for _, m := range want[kept:] {
		cmds = append(cmds, "announcements add "+m)
	}
	return cmds
}

// customVoteCommands plans the commands that change custom votes' limits,
// for votes that are otherwise the same (sameVote): "votes <name> ...".
func customVoteCommands(cur, want []CustomVote) []string {
	var cmds []string
	for i, w := range want {
		c := cur[i]
		pre := "votes " + w.Name + " "
		if c.Enabled != w.Enabled {
			cmds = append(cmds, pre+onOff(w.Enabled))
		}
		if c.Percent != w.Percent {
			cmds = append(cmds, pre+strconv.Itoa(w.Percent))
		}
		if c.Seconds != w.Seconds {
			cmds = append(cmds, pre+"seconds "+strconv.Itoa(w.Seconds))
		}
		if c.Cooldown != w.Cooldown {
			cmds = append(cmds, pre+"cooldown "+strconv.Itoa(w.Cooldown))
		}
		if c.MinPlayers != w.MinPlayers {
			cmds = append(cmds, pre+"min-players "+strconv.Itoa(w.MinPlayers))
		}
	}
	return cmds
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
	av, aok := a.([]CustomVote)
	bv, bok := b.([]CustomVote)
	if aok || bok {
		return aok && bok && slices.EqualFunc(av, bv, func(x, y CustomVote) bool {
			return sameVote(x, y) && x.Enabled == y.Enabled && x.Percent == y.Percent && x.Seconds == y.Seconds &&
				x.Cooldown == y.Cooldown && x.MinPlayers == y.MinPlayers
		})
	}
	return a == b
}

// sameVote reports whether two custom votes are the same vote: what only a
// restart changes, as no command adds, removes or rewrites one.
func sameVote(x, y CustomVote) bool {
	return x.Name == y.Name && x.Description == y.Description && x.Command == y.Command && slices.Equal(x.Choices, y.Choices)
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
