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

	"github.com/xThrasherrr/ReSkateManager/internal/atomicfile"
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

	roles    map[string]any // data/roles.json, on a server with roles (roles.go)
	rolesSet bool           // they changed, so Write writes data/roles.json
}

// maxConfig caps how much of a ReSkateServer.json is read. The server's own
// is a few KB; one from a restore or an import could be anything.
const maxConfig = 16 << 20

// BansFile is where a server from ReSkate 1.1.7 on keeps its bans, relative
// to its folder.
const BansFile = "data/bans.json"

// HostFiles are the files in a server's folder that are the host's own: what
// it can't download again, apart from its mods. A server update creates them
// when missing but never replaces them, and a backup holds them. Servers from
// ReSkate 1.1.7 on keep their bans in data/, and from 2.0.6 their roles (the
// admins among them) and chat words too.
var HostFiles = []string{"ReSkateServer.json", "world-layers.json", BansFile, RolesFile,
	"data/words_filtered.txt", "data/words_blocked.txt"}

// Read reads a ReSkateServer.json, which must hold one JSON object, and the
// bans in data/bans.json and roles in data/roles.json beside it.
func Read(path string) (*File, error) {
	data, err := readCapped(path, maxConfig)
	if err != nil {
		return nil, err
	}
	dec := decodeJSON(data)
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
	if f.roles, err = readRoles(path); err != nil {
		return nil, err
	}
	data, err = readCapped(filepath.Join(filepath.Dir(path), BansFile), maxConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	// The server won't start with bans it can't read either.
	var rows []any
	if err := decodeJSON(data).Decode(&rows); err != nil {
		return nil, fmt.Errorf("%s is not a JSON list: %w", BansFile, err)
	}
	f.bans, f.bansFile = bansOf(rows), true
	return f, nil
}

// decodeJSON decodes data with its numbers kept as json.Number, so a whole
// number is written back as it was.
func decodeJSON(data []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec
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
// writes them, so a config that held them loses them only once they're safe;
// and so do changed roles, for the admins a config listed.
func (f *File) Write(path string) error {
	data, err := f.Bytes()
	if err != nil {
		return err
	}
	if f.bansSet && f.bansFile {
		if err := writeJSON(filepath.Join(filepath.Dir(path), BansFile), banRows(f.bans)); err != nil {
			return err
		}
	}
	if f.rolesSet {
		if err := f.WriteRoles(path); err != nil {
			return err
		}
	}
	return atomicfile.Write(path, data, 0o600)
}

// writeJSON writes v indented, as the server writes its files in data/, in
// one step like Write.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

// sections says where a sectioned file keeps each flat key (load_config in
// Server/server_config.cpp, 1.1.7). A nested key goes by its first part, so
// "distances.half_rate_start" is in network.distances. Keys not listed, the
// votes, 2.0.2's announcements and 2.0.3's commands, are in the same place in
// both layouts.
var sections = map[string]string{
	"name": "server.name", "password": "server.password", "welcome": "server.welcome_message", "listed": "server.listed",
	"max_players": "server.max_players", "port": "server.port", "query_port": "server.query_port",
	"steam_token": "server.steam_token", "auto_update": "server.auto_update", "activity_log": "server.activity_log",
	// Whether the Windows server's window (2.0.6) starts it at once. The
	// manager runs it without one, so it's left as it is.
	"autostart":  "server.autostart",
	"chat_color": "server.chat_color", "chat_text_color": "server.chat_text_color",

	"admins": "access.admins", "reserved": "access.reserved_players_slots", "global_bans": "access.use_global_bans",

	"map": "maps.map", "map_pool": "maps.pool", "map_rotation_minutes": "maps.rotation_minutes", "parks": "maps.parks",
	"world_layer_sync": "maps.world_layer_sync", "layers": "maps.layers", "park_mods": "maps.park_mods", "park_limits": "maps.park_limits",

	"boosts": "players.allow_boosts", "no_bail": "players.allow_no_bail", "noclip": "players.allow_noclip",
	"parties": "players.allow_parties", "party_size": "players.party_size", "voice_chat": "players.allow_voice_chat",
	"voice_range": "players.voice_range", "object_placement": "players.object_placement",
	"object_limit": "players.object_limit", "announce_throwdowns": "players.announce_throwdowns",
	"afk_kick_minutes": "players.afk_kick_minutes", "allow_object_scaling": "players.allow_object_scaling", "sync_effects": "players.sync_effects",
	"word_warnings": "players.word_warnings", "use_global_word_filter": "players.use_global_word_filter",

	"speed_check": "anti_cheat.speed_hack", "score_check": "anti_cheat.modified_scoring",
	"score_allow": "anti_cheat.allowed_scoring_mods", "enforce_tuning": "anti_cheat.enforce_tuning",
	"bone_scale_limit": "anti_cheat.bone_scale_limit", "bone_reach_limit": "anti_cheat.bone_reach_limit", "pinned_board": "anti_cheat.pinned_board",

	"send_rate": "network.send_rate", "crowd_budget": "network.crowd_budget", "distances": "network.distances",
	"use_steam_relay": "network.use_steam_relay", "pack_ms": "network.pack_ms", "finger_distance": "network.finger_distance",
	"steam_debug": "network.steam_debug", "threads": "network.threads",
}

// removed are the settings newer servers no longer have, by the release that
// took each out and how to tell a file it wrote. Reserved slots went with the
// sections, as slots are kept for each listed player instead. The card went
// in 2.0.3, which shows every announcement as one; that server writes its
// announcements without it, and an older one with it or not at all. The
// admins went into the admin role with 2.0.6's roles.
var removed = map[string]struct {
	since string
	gone  func(*File) bool
}{
	"reserved_slots": {"1.1.7", (*File).Sectioned},
	"announcements.card": {"2.0.3", func(f *File) bool {
		_, card := f.Get("announcements.card")
		return !card && f.announces()
	}},
	"admins": {"2.0.6", (*File).HasRoles},
}

// Removed lists the settings the newest servers no longer have.
var Removed = slices.Sorted(maps.Keys(removed))

// Gone lists the settings the server that wrote the file no longer has.
func (f *File) Gone() []string {
	out := []string{}
	for _, key := range Removed {
		if removed[key].gone(f) {
			out = append(out, key)
		}
	}
	return out
}

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
	return err == nil && f.announces()
}

// announces reports whether the file has the announcements of ReSkate 2.0.2
// on. Their card went in 2.0.3; the timer is in both.
func (f *File) announces() bool {
	_, ok := f.Get("announcements.interval_minutes")
	return ok
}

// CommandRuns says how many server commands each of the owner's chat commands
// in the config at path runs, by its name.
func CommandRuns(path string) map[string]int {
	out := map[string]int{}
	f, err := Read(path)
	if err != nil {
		return out
	}
	list, _ := f.Values()["commands"].([]CustomCommand)
	for _, c := range list {
		out[c.Name] = len(c.Command)
	}
	return out
}

// Ban is one of the server's bans. Admins and bans have their own pages.
type Ban struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Added int64  `json:"added"`
}

// Admins lists the in-game admins, by SteamID64: those the config lists,
// and on a server with roles those given the admin role, which the server
// moves the config's into when it starts.
func (f *File) Admins() []string {
	out := []string{}
	v, _ := f.Get("admins")
	list, _ := v.([]any)
	for _, v := range list {
		out = append(out, fmt.Sprint(v))
	}
	given := f.rolePlayers()
	for _, id := range slices.Sorted(maps.Keys(given)) {
		if given[id] == AdminRole && !slices.Contains(out, id) {
			out = append(out, id)
		}
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

// SetAdmins replaces the in-game admins: on a server with roles, those given
// the admin role in data/roles.json; anyone it's taken from is the default
// role again.
func (f *File) SetAdmins(ids []string) error {
	if f.roles != nil {
		return f.setAdminRole(ids)
	}
	f.Set("admins", anys(ids))
	return nil
}

// SetBans replaces the bans: in data/bans.json for a server that keeps them
// there, else in the config.
func (f *File) SetBans(bans []Ban) {
	if f.bansFile || f.Sectioned() {
		f.bans, f.bansFile, f.bansSet = slices.Clone(bans), true, true
		delete(f.Root, "bans")
		return
	}
	f.Root["bans"] = banRows(bans)
}

// banRows are bans as the server writes them, in either place.
func banRows(bans []Ban) []any {
	rows := make([]any, len(bans))
	for i, b := range bans {
		rows[i] = map[string]any{"id": b.ID, "name": b.Name, "added": b.Added}
	}
	return rows
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
		if fd.Type == TypeNumber {
			// The server keeps these as floats and writes 0.7 back as
			// 0.699999988079071, so both are read as the float they make.
			n, _ = strconv.ParseFloat(strconv.FormatFloat(n, 'g', -1, 32), 64)
		}
		if n < fd.Min || (fd.Max != 0 && n > fd.Max) {
			return nil, fmt.Errorf("%s must be %g to %g", fd.Label, fd.Min, fd.Max)
		}
		if z, ok := zeroOr[fd.Key]; ok && n != 0 && n < z.least {
			return nil, fmt.Errorf("%s must be 0 (%s) or %g to %g", fd.Label, z.zero, z.least, fd.Max)
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
		items, ok := anyList(raw)
		if !ok {
			return nil, fmt.Errorf("%s must be a list", fd.Label)
		}
		out := []string{}
		for _, x := range items {
			it := strings.ToLower(strings.TrimSpace(fmt.Sprint(x)))
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
		items, ok := anyList(raw)
		if !ok {
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
		items, ok := anyList(raw)
		if !ok {
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
	case TypeCommands:
		return customCommands(raw)
	case TypeParkMods:
		return parkMods(raw)
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

// anyList reads a list as JSON or the panel gives it.
func anyList(raw any) ([]any, bool) {
	switch v := raw.(type) {
	case []any:
		return v, true
	case []string:
		return anys(v), true
	}
	return nil, false
}

// anys is a list of strings as the file holds it: [] when empty, never null.
func anys(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// zeroOr are the numbers whose 0 stands for something, no limit or the votes'
// own length, and that otherwise start above their minimum: the least they
// are then, and what 0 is.
var zeroOr = map[string]struct {
	least float64
	zero  string
}{
	"crowd_budget":              {300, "no limit"},
	"bone_scale_limit":          {1, "no limit"},
	"bone_reach_limit":          {0.5, "no limit"},
	"votes.map.seconds":         {minVoteSeconds, "the vote length"},
	"votes.kick.seconds":        {minVoteSeconds, "the vote length"},
	"votes.time_of_day.seconds": {minVoteSeconds, "the vote length"},
}

// Limits of 2.0.2's custom votes (Server/server_config.h,
// Extension/Multiplayer/Net/protocol.h).
const (
	maxCustomVotes   = 16
	maxVoteChoices   = 8
	maxVoteNameBytes = 16
	maxVoteDescBytes = 80
	maxAdminText     = 320 // the longest server command a vote or chat command runs
	defaultVotePct   = 60
	minVoteSeconds   = 10
	maxVoteSeconds   = 300
	maxVoteCooldown  = 3600
	commandExample   = `e.g. "map {map}"`
)

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
		if v.Name, err = textOf(m, "name", "a custom vote"); err != nil {
			return nil, err
		}
		if v.Name = strings.ToLower(strings.TrimSpace(v.Name)); !validVoteName(v.Name) {
			return nil, fmt.Errorf("a custom vote's name is 1 to %d lowercase letters, digits, - or _ (%q is not)", maxVoteNameBytes, v.Name)
		}
		if !voteNameFree(v.Name) {
			return nil, fmt.Errorf("custom vote %q: that name is one of the server's own votes", v.Name)
		}
		if slices.ContainsFunc(out, func(o CustomVote) bool { return o.Name == v.Name }) {
			return nil, fmt.Errorf("two custom votes are called %q", v.Name)
		}
		if v.Description, err = textOf(m, "description", "a custom vote"); err != nil {
			return nil, err
		}
		if v.Description = strings.TrimSpace(v.Description); v.Description != "" && (len(v.Description) > maxVoteDescBytes || !ValidChatText(v.Description)) {
			return nil, fmt.Errorf("custom vote %q: the description must be one chat line of at most %d bytes", v.Name, maxVoteDescBytes)
		}
		if v.Command, err = textOf(m, "command", "a custom vote"); err != nil {
			return nil, err
		}
		if v.Command = strings.TrimSpace(v.Command); !validAdminText(v.Command) {
			return nil, fmt.Errorf("custom vote %q: the command must be one server command of at most %d bytes, %s", v.Name, maxAdminText, commandExample)
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

// textOf reads one of an object's texts as load_config does: a missing one,
// or null, is empty. what names the object in the error, as "a custom vote".
func textOf(m map[string]any, key, what string) (string, error) {
	s, ok := m[key].(string)
	if !ok && m[key] != nil {
		return "", fmt.Errorf("%s's %s must be text", what, key)
	}
	return s, nil
}

// validAdminText is valid_admin_text: one server command of at most 320
// bytes of UTF-8, with no control characters.
func validAdminText(s string) bool {
	return s != "" && len(s) <= maxAdminText && utf8.ValidString(s) && !strings.ContainsFunc(s, isControl)
}

// Limits of 2.0.3's chat commands (Server/server_config.h).
const (
	maxCustomCommands = 32
	maxCommandRuns    = 8
)

// commandNameFree is custom_command_name_free: not one of the chat's own
// commands, nor a server command admins type as /<command>, and not a
// number, which answers a poll.
func commandNameFree(s string) bool {
	if slices.Contains([]string{"help", "party", "p", "w", "whisper", "tell", "poll", "vote", "yes", "y", "no", "n", "tp",
		"activity-log", "admin", "admins", "afk-kick", "announce", "announce-throwdowns", "announce-to", "announcements",
		"ban", "bans", "bone-scale", "boosts", "boosts-allow", "chat-color", "chat-colour", "clear-objects", "crowd",
		"distances", "effects", "kick", "layer", "layer-sync", "layers", "listed", "map", "map-pool", "maps", "msg",
		"msg-admins", "msg-party", "name", "net", "nobail", "nobail-allow", "noclip", "noclip-allow", "object-limit",
		"object-placement", "object-scaling", "objects", "park", "parties", "party-size", "password", "placement",
		"players", "rate", "reserved", "rotation", "say", "score-allow", "score-check", "speed-check", "status", "tod",
		"tpall", "tphere", "tps", "tuning", "tuning-enforce", "unban", "voice", "voice-allow", "voice-range",
		"vote-cancel", "votes", "welcome", "world-layer-sync", "quit", "exit", "stop", "update",
		"role", "roles", "board-check", "park-mod", "teleport"}, s) { // 2.0.6
		return false
	}
	return strings.Trim(s, "0123456789") != ""
}

// customCommands reads commands as load_config does (a command is one string
// or a list of them) and checks them as custom_commands_error does, since the
// server won't start on one it refuses.
func customCommands(raw any) ([]CustomCommand, error) {
	if list, ok := raw.([]CustomCommand); ok {
		raw = jsonValue(Field{Type: TypeCommands}, list)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("chat commands must be a list")
	}
	if len(items) > maxCustomCommands {
		return nil, fmt.Errorf("at most %d chat commands", maxCustomCommands)
	}
	out := []CustomCommand{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("each chat command must be an object")
		}
		c := CustomCommand{Command: []string{}}
		var err error
		if c.Name, err = textOf(m, "name", "a chat command"); err != nil {
			return nil, err
		}
		if c.Name = strings.ToLower(strings.TrimSpace(c.Name)); !validVoteName(c.Name) {
			return nil, fmt.Errorf("a chat command's name is 1 to %d lowercase letters, digits, - or _ (%q is not)", maxVoteNameBytes, c.Name)
		}
		if !commandNameFree(c.Name) {
			return nil, fmt.Errorf("chat command /%s: that name is one of the server's own commands", c.Name)
		}
		if slices.ContainsFunc(out, func(o CustomCommand) bool { return o.Name == c.Name }) {
			return nil, fmt.Errorf("two chat commands are called /%s", c.Name)
		}
		if c.Reply, err = textOf(m, "reply", "a chat command"); err != nil {
			return nil, err
		}
		if c.Reply = strings.TrimSpace(c.Reply); c.Reply != "" && !ValidChatText(c.Reply) {
			return nil, fmt.Errorf("chat command /%s: the reply must be one chat line of at most 200 bytes", c.Name)
		}
		var runs []any
		switch v := m["command"].(type) {
		case nil:
		case string:
			runs = []any{v} // one, which can't be empty
		case []any:
			for _, x := range v {
				if s, _ := x.(string); strings.TrimSpace(s) != "" { // load_config skips the rest
					runs = append(runs, s)
				}
			}
		default:
			return nil, fmt.Errorf("chat command /%s: the command must be text or a list of them", c.Name)
		}
		for _, x := range runs {
			if s := strings.TrimSpace(x.(string)); validAdminText(s) {
				c.Command = append(c.Command, s)
			} else {
				return nil, fmt.Errorf("chat command /%s: each command must be one server command of at most %d bytes, e.g. \"announce-to {player} Hi\"", c.Name, maxAdminText)
			}
		}
		if len(c.Command) > maxCommandRuns {
			return nil, fmt.Errorf("chat command /%s: at most %d server commands", c.Name, maxCommandRuns)
		}
		if c.Reply == "" && len(c.Command) == 0 {
			return nil, fmt.Errorf("chat command /%s: it needs a reply, a command, or both", c.Name)
		}
		if x, ok := m["admin"]; ok {
			if c.Admin, ok = x.(bool); !ok {
				return nil, fmt.Errorf("chat command /%s: admin must be true or false", c.Name)
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Limits of 2.0.6's park mods (Server/server_config.h).
const maxParkMods = 32

// validModFolder is valid_mod_folder: a folder's name in Mods as the game's
// mod list takes it, with no path and no blanks at either end.
func validModFolder(s string) bool {
	if s == "" || len(s) > 64 || s[0] == '.' || s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("_-. ", r))
	})
}

// validParkKey is valid_park_key: a base map's key as park files are named
// by it (bam, grom, stadium_1...).
func validParkKey(s string) bool {
	return s != "" && len(s) <= 32 && !strings.ContainsFunc(s, func(r rune) bool {
		return !('a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '_')
	})
}

// parkMods reads park mods as load_config does and checks them as
// park_mods_error does, since the server won't start on one it refuses. That
// a map is one the server knows is checked against its maps, in Diff.
func parkMods(raw any) ([]ParkMod, error) {
	if list, ok := raw.([]ParkMod); ok {
		raw = jsonValue(Field{Type: TypeParkMods}, list)
	}
	if raw == nil {
		return []ParkMod{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("park mods must be a list")
	}
	if len(items) > maxParkMods {
		return nil, fmt.Errorf("at most %d park mods", maxParkMods)
	}
	out := []ParkMod{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("each park mod must be an object")
		}
		var pm ParkMod
		var err error
		if pm.Name, err = textOf(m, "name", "a park mod"); err != nil {
			return nil, err
		}
		if pm.Name = strings.ToLower(strings.TrimSpace(pm.Name)); !validVoteName(pm.Name) {
			return nil, fmt.Errorf("a park mod's name is 1 to %d lowercase letters, digits, - or _ (%q is not)", maxVoteNameBytes, pm.Name)
		}
		if slices.ContainsFunc(out, func(o ParkMod) bool { return o.Name == pm.Name }) {
			return nil, fmt.Errorf("two park mods are called %s", pm.Name)
		}
		if pm.Folder, err = textOf(m, "folder", "a park mod"); err != nil {
			return nil, err
		}
		if !validModFolder(pm.Folder) {
			return nil, fmt.Errorf("park mod %s: the folder is the name of a mod's folder in Mods: letters, digits, spaces, _ - and ., no path", pm.Name)
		}
		if pm.Map, err = textOf(m, "map", "a park mod"); err != nil {
			return nil, err
		}
		if pm.Map = strings.TrimSpace(pm.Map); pm.Map == "" {
			return nil, fmt.Errorf("park mod %s: name the map it was built on", pm.Name)
		}
		if pm.Key, err = textOf(m, "key", "a park mod"); err != nil {
			return nil, err
		}
		if pm.Key = strings.TrimSpace(pm.Key); pm.Key != "" && !validParkKey(pm.Key) {
			return nil, fmt.Errorf("park mod %s: the key is a base map's, such as bam, grom or stadium_1, or empty", pm.Name)
		}
		out = append(out, pm)
	}
	return out, nil
}

// parkModCommands plans the park-mod config commands that turn the park mods
// from cur into want. A changed one is removed and set up again.
func parkModCommands(cur, want []ParkMod) []string {
	remove := func(pm ParkMod) string { return "park-mod config remove " + pm.Name }
	add := func(pm ParkMod) string {
		cmd := "park-mod config add " + pm.Name + " | " + pm.Folder + " | " + pm.Map
		if pm.Key != "" {
			cmd += " | " + pm.Key
		}
		return cmd
	}
	return setCommands(cur, want, remove, add)
}

// setCommands plans the commands that turn the set cur into want, as the
// server only adds and removes one at a time: what want lacks is removed,
// then what cur lacks is added, each in its list's order.
func setCommands[T comparable](cur, want []T, remove, add func(T) string) []string {
	var cmds []string
	for _, x := range cur {
		if !slices.Contains(want, x) {
			cmds = append(cmds, remove(x))
		}
	}
	for _, x := range want {
		if !slices.Contains(cur, x) {
			cmds = append(cmds, add(x))
		}
	}
	return cmds
}

// withArg is a command that takes the argument it's given at its end.
func withArg(cmd string) func(string) string {
	return func(arg string) string { return cmd + arg }
}

// jsonValue is how a normalised value is stored in the file.
func jsonValue(fd Field, v any) any {
	switch fd.Type {
	case TypeInt:
		return json.Number(strconv.FormatInt(int64(v.(float64)), 10))
	case TypeNumber:
		return json.Number(strconv.FormatFloat(v.(float64), 'f', -1, 64))
	case TypeList, TypeMaps, TypeLines:
		return anys(v.([]string))
	case TypeVotes, TypeCommands, TypeParkMods:
		return jsonTree(v)
	}
	return v
}

// jsonTree is a list of the owner's custom votes, chat commands or park mods
// as the server writes them: every key of their json tags, so it adds none on
// its next start, a chat command's command always a list, and whole numbers
// as json.Number, as Read keeps them.
func jsonTree(v any) []any {
	data, _ := json.Marshal(v) // structs of text, numbers and switches: it can't fail
	var list []any
	decodeJSON(data).Decode(&list)
	if list == nil {
		list = []any{}
	}
	return list
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
	curVals, gone := cur.Values(), cur.Gone()
	p := &Plan{Restart: map[string]any{}, Changed: map[string]any{}}
	for key, raw := range want {
		fd, ok := FieldByKey(key)
		if !ok {
			return nil, fmt.Errorf("unknown setting %q", key)
		}
		if slices.Contains(gone, key) {
			return nil, fmt.Errorf("the server no longer has %s: ReSkate %s took it out", strings.ToLower(fd.Label), removed[key].since)
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
		if fd.Type == TypeParkMods {
			if v, err = parkMaps(v.([]ParkMod), known); err != nil {
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
		case "map_pool":
			old, _ := curVals[fd.Key].([]string)
			cmds, err := poolCommands(old, v.([]string), known)
			if err != nil {
				return nil, err
			}
			p.Commands = append(p.Commands, cmds...)
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
		case "reserved":
			old, _ := curVals[fd.Key].([]string)
			p.Commands = append(p.Commands, setCommands(old, v.([]string), withArg("reserved remove "), withArg("reserved add "))...)
		case "distances.full_rate_return", "distances.half_rate_start", "distances.half_rate_return", "distances.low_rate_start":
			if !distances {
				distances = true
				p.Commands = append(p.Commands, fmt.Sprintf("distances %s %s %s %s",
					num(merged["distances.full_rate_return"]), num(merged["distances.half_rate_start"]),
					num(merged["distances.half_rate_return"]), num(merged["distances.low_rate_start"])))
			}
		case "announcements.messages":
			old, _ := curVals[fd.Key].([]string)
			p.Commands = append(p.Commands, announcementCommands(old, v.([]string))...)
		case "votes.custom":
			old, _ := curVals[fd.Key].([]CustomVote)
			p.Commands = append(p.Commands, customVoteCommands(old, v.([]CustomVote))...)
		case "park_mods":
			old, _ := curVals[fd.Key].([]ParkMod)
			p.Commands = append(p.Commands, parkModCommands(old, v.([]ParkMod))...)
		case "score_allow":
			old, _ := curVals[fd.Key].([]string)
			p.Commands = append(p.Commands, setCommands(old, v.([]string), withArg("score-allow remove "), withArg("score-allow "))...)
		default:
			cmd, ok := settingCommand(fd, v)
			if !ok {
				return nil, fmt.Errorf("no command for %s", fd.Key)
			}
			p.Commands = append(p.Commands, cmd)
		}
	}
	return p, nil
}

// commandVerbs are the console commands that set a setting, by its key, with
// one "<verb> <value>": on or off for a switch, the text or choice itself, or
// the number.
var commandVerbs = map[string]string{
	"name": "name", "map": "map", "listed": "listed", "reserved_slots": "reserved slots",
	"map_rotation_minutes": "rotation", "send_rate": "rate", "crowd_budget": "crowd",
	"voice_chat": "voice", "voice_range": "voice-range",
	"object_placement": "placement", "object_limit": "objects", "allow_object_scaling": "object-scaling",
	"bone_scale_limit": "bone-scale", "bone_reach_limit": "bone-reach", "sync_effects": "effects",
	"noclip": "noclip", "no_bail": "nobail", "boosts": "boosts", "enforce_tuning": "tuning",
	"parties": "parties", "party_size": "party-size", "afk_kick_minutes": "afk-kick",
	"announce_throwdowns": "announce-throwdowns", "activity_log": "activity-log",
	"parks.construction": "park construction", "parks.historic": "park historic", "parks.financial": "park financial",
	"announcements.interval_minutes": "announcements interval", "announcements.card": "announcements card",
	"votes.seconds": "votes seconds", "votes.cooldown_seconds": "votes cooldown", "votes.starter_votes_yes": "votes starter-yes",
	"votes.map.enabled": "votes map", "votes.map.percent": "votes map", "votes.map.seconds": "votes map seconds",
	"votes.map.cooldown_seconds": "votes map cooldown", "votes.map.min_players": "votes map min-players",
	"votes.kick.enabled": "votes kick", "votes.kick.percent": "votes kick", "votes.kick.seconds": "votes kick seconds",
	"votes.kick.cooldown_seconds": "votes kick cooldown", "votes.kick.min_players": "votes kick min-players",
	"votes.time_of_day.enabled": "votes tod", "votes.time_of_day.percent": "votes tod", "votes.time_of_day.seconds": "votes tod seconds",
	"votes.time_of_day.cooldown_seconds": "votes tod cooldown", "votes.time_of_day.min_players": "votes tod min-players",
	"votes.polls": "votes polls", "votes.poll_seconds": "votes poll-seconds",
	"speed_check": "speed-check", "pinned_board": "board-check", "score_check": "score-check",
	"use_global_word_filter": "words global", "world_layer_sync": "layer-sync",
}

// offAtZero are the numbers whose 0 the server takes as "<verb> off".
var offAtZero = []string{"map_rotation_minutes", "crowd_budget", "object_limit", "bone_scale_limit", "bone_reach_limit",
	"afk_kick_minutes", "announcements.interval_minutes"}

// settingCommand is the command that sets fd to v, when commandVerbs has one.
func settingCommand(fd Field, v any) (string, bool) {
	verb, ok := commandVerbs[fd.Key]
	if !ok {
		return "", false
	}
	switch fd.Type {
	case TypeBool:
		return verb + " " + onOff(v), true
	case TypeInt, TypeNumber:
		if v.(float64) == 0 && slices.Contains(offAtZero, fd.Key) {
			return verb + " off", true
		}
		return verb + " " + num(v), true
	}
	return verb + " " + v.(string), true
}

// SettingVerbs are the console commands that change settings: every one Diff
// plans, plus the world page's tod, layers and layer, tps, which servers
// before 1.1.5 still take, and the names the server also takes for some of
// them (the in-game menu's, and chat-colour). Typed into the console, they
// need the same permission as the settings pages.
var SettingVerbs = func() []string {
	// What Diff plans besides commandVerbs, then the others.
	out := []string{"map-pool", "password", "welcome", "chat-color", "distances", "park-mod", "score-allow",
		"tps", "tod", "layers", "layer", "chat-colour",
		"voice-allow", "object-placement", "object-limit", "world-layer-sync", "noclip-allow", "nobail-allow", "boosts-allow", "tuning-enforce"}
	for _, verb := range commandVerbs {
		first, _, _ := strings.Cut(verb, " ")
		out = append(out, first)
	}
	slices.Sort(out)
	return slices.Compact(out)
}()

// parkMaps checks that each park mod's map is one of the server's maps, and
// spells it as the server does: it won't start on a park mod for another.
func parkMaps(list []ParkMod, known []string) ([]ParkMod, error) {
	out := slices.Clone(list)
	for i, pm := range out {
		m, err := knownMaps([]string{pm.Map}, known)
		if err != nil {
			return nil, fmt.Errorf("park mod %s: %w", pm.Name, err)
		}
		out[i].Map = m[0]
	}
	return out, nil
}

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
	ac, aok := a.([]CustomCommand)
	bc, bok := b.([]CustomCommand)
	if aok || bok {
		return aok && bok && slices.EqualFunc(ac, bc, func(x, y CustomCommand) bool {
			return x.Name == y.Name && x.Reply == y.Reply && slices.Equal(x.Command, y.Command) && x.Admin == y.Admin
		})
	}
	ap, aok := a.([]ParkMod)
	bp, bok := b.([]ParkMod)
	if aok || bok {
		return aok && bok && slices.Equal(ap, bp)
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
