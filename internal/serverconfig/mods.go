package serverconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// RetailMaps are the maps every server knows, named as its maps command
// lists them (load_levels in Server/server_config.cpp).
var RetailMaps = []string{"San Vansterdam", "Isle of Grom", "Super Ultra Mega Resort", "Tutorial Island", "Stadium 1", "Stadium 2"}

var retailAssets = []string{
	"Levels/Game/BAM_LevelRoot/BAM_LevelRoot",
	"Levels/Game/DingoLevel_Isle_of_Grom/DingoLevel_Isle_of_Grom",
	"Levels/Game/DingoLevel_MPR/DingoLevel_MPR",
	"Levels/Game/DingoLevel_FTUE_Island/DingoLevel_FTUE_Island",
	"Levels/Game/DingoLevel_SDM/DingoLevel_SDM_Int_001/DingoLevel_SDM_Int_001",
	"Levels/Game/DingoLevel_SDM/DingoLevel_SDM_Int_002/DingoLevel_SDM_Int_002",
}

// Mod is one folder in the server's Mods folder. The server reads only its
// reskate-levels.json; the rest is shown so staff can tell mods apart.
type Mod struct {
	Folder      string   `json:"folder"`
	Title       string   `json:"title"`
	Author      string   `json:"author,omitempty"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Maps        []ModMap `json:"maps"`
	// Why the server skips the mod's maps, or empty.
	Problem string `json:"problem,omitempty"`
	// Moved to DisabledMods, where the server does not look.
	Disabled bool `json:"disabled,omitempty"`
	// A link to the shared mods, where it is updated and deleted.
	Shared bool `json:"shared,omitempty"`

	manifestName string
	// The Thunderstore package it came from as Namespace-Name, when the
	// folder or manifest says.
	Package string `json:"package,omitempty"`
}

// ModMap is a map a mod adds, as its reskate-levels.json lists it.
type ModMap struct {
	Name  string `json:"name"`
	Asset string `json:"asset"`
	// Another mod (or a retail map) registers the same level first, so the
	// server ignores this one.
	Shadowed bool `json:"shadowed,omitempty"`
}

// ModsDir is where the server looks for map mods.
func ModsDir(dir string) string { return filepath.Join(dir, "Mods") }

// DisabledModsDir holds mods taken out of Mods without deleting them.
func DisabledModsDir(dir string) string { return filepath.Join(dir, "DisabledMods") }

// ReadMods lists the mod folders in dir/Mods in the order the server reads
// them, then the disabled ones. A missing folder is no mods.
func ReadMods(dir string) ([]Mod, error) {
	seen := map[string]bool{}
	for _, a := range retailAssets {
		seen[foldPath(a)] = true
	}
	mods := []Mod{}
	for _, disabled := range []bool{false, true} {
		parent := ModsDir(dir)
		if disabled {
			parent = DisabledModsDir(dir)
		}
		entries, err := os.ReadDir(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			path := filepath.Join(parent, e.Name())
			shared := false
			if !e.IsDir() {
				// The server follows a link to a folder, but skips a broken one.
				if fi, err := os.Stat(path); linkTarget(path) == "" || err != nil || !fi.IsDir() {
					continue
				}
				shared = isSharedLink(path)
			}
			m := Mod{Folder: e.Name(), Title: e.Name(), Maps: []ModMap{}, Disabled: disabled, Shared: shared}
			readModInfo(path, &m)
			levels, err := readLevels(filepath.Join(path, "reskate-levels.json"))
			if err != nil {
				m.Problem = err.Error()
			}
			for _, l := range levels {
				// The server never reads a disabled mod, so it shadows nothing.
				if !disabled {
					key := foldPath(l.Asset)
					l.Shadowed = seen[key]
					seen[key] = true
				}
				m.Maps = append(m.Maps, l)
			}
			mods = append(mods, m)
		}
	}
	return mods, nil
}

// KnownMaps lists the maps the server knows once it next starts: the retail
// ones, then each enabled mod's (load_levels in Server/server_config.cpp).
func KnownMaps(dir string) ([]string, error) {
	mods, err := ReadMods(dir)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(RetailMaps)
	for _, m := range mods {
		if m.Disabled {
			continue
		}
		for _, l := range m.Maps {
			if !l.Shadowed {
				out = append(out, l.Name)
			}
		}
	}
	return out, nil
}

// readModInfo fills the title and credits from manifest.json, or the older
// reskate-mod.json, read loosely like the game does.
func readModInfo(path string, m *Mod) {
	defer func() { m.Package = packageName(m.Folder, m.Author, m.manifestName) }()
	for _, name := range []string{"manifest.json", "reskate-mod.json"} {
		data, err := readCapped(filepath.Join(path, name), maxModJSON)
		if err != nil {
			continue
		}
		var info map[string]any
		if json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), &info) != nil {
			continue
		}
		str := func(k string) string { s, _ := info[k].(string); return strings.TrimSpace(s) }
		if t := str("name"); t != "" {
			m.Title = t
			if name == "manifest.json" {
				m.manifestName = t
			}
		}
		m.Author, m.Description = str("author"), str("description")
		if m.Version = str("version_number"); m.Version == "" {
			m.Version = str("version")
		}
		return
	}
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// maxModJSON caps the JSON files read from a mod, which comes from anyone: a
// real one is a few KB, and the mods page reads them often.
const maxModJSON = 1 << 20

// packageName works out the Thunderstore package (Namespace-Name) a mod came
// from. Thunderstore's manifest.json names the package but not its team, which
// the folder carries instead: mod managers and Thunderstore's own downloads
// name it Namespace-Name-1.2.3.
func packageName(folder, author, name string) string {
	if name == "" {
		// No manifest: trust only the full Namespace-Name-1.2.3 shape.
		parts := strings.Split(folder, "-")
		if len(parts) == 3 && parts[0] != "" && parts[1] != "" && isVersion(parts[2]) {
			return parts[0] + "-" + parts[1]
		}
		return ""
	}
	if strings.ContainsAny(name, "- ") {
		return "" // not a Thunderstore name
	}
	if ns, rest, ok := strings.Cut(folder, "-"); ok && ns != "" {
		if strings.EqualFold(rest, name) {
			return ns + "-" + name
		}
		if after, ok := strings.CutPrefix(strings.ToLower(rest), strings.ToLower(name)+"-"); ok && isVersion(after) {
			return ns + "-" + name
		}
	}
	// Some manifests name the team themselves.
	if author != "" && !strings.ContainsAny(author, "- ") {
		return author + "-" + name
	}
	return ""
}

func isVersion(s string) bool {
	_, ok := ParseVersion(s)
	return ok
}

// ParseVersion reads a dotted version such as 1.2.3.
func ParseVersion(s string) ([]int, bool) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, len(out) > 0
}

// NewerVersion reports whether version a is newer than b. Versions that do not
// parse are never newer.
func NewerVersion(a, b string) bool {
	x, ok1 := ParseVersion(a)
	y, ok2 := ParseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < max(len(x), len(y)); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return p > q
		}
	}
	return false
}

// readLevels reads a reskate-levels.json as the server does: every level needs
// an asset, and displayName falls back to a name made from the asset. A mod
// without the file has no maps.
func readLevels(file string) ([]ModMap, error) {
	data, err := readCapped(file, maxModJSON)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseLevels(data)
}

func parseLevels(data []byte) ([]ModMap, error) {
	var root struct {
		Levels []struct {
			Asset       *string `json:"asset"`
			DisplayName string  `json:"displayName"`
		} `json:"levels"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), &root); err != nil {
		return nil, fmt.Errorf("reskate-levels.json is not valid JSON: %w", err)
	}
	if root.Levels == nil {
		return nil, errors.New("reskate-levels.json has no levels list")
	}
	out := make([]ModMap, 0, len(root.Levels))
	for _, l := range root.Levels {
		if l.Asset == nil {
			return out, errors.New("a level in reskate-levels.json has no asset")
		}
		name := l.DisplayName
		if name == "" {
			name = levelName(*l.Asset)
		}
		out = append(out, ModMap{Name: name, Asset: *l.Asset})
	}
	return out, nil
}

// levelName and levelShortName follow world_level_name in
// Engine/Game/World/world_names.h.
func levelName(asset string) string {
	name := levelShortName(asset)
	switch strings.ToLower(name) {
	case "bam":
		return "San Vansterdam"
	case "ftue island":
		return "Tutorial Island"
	case "mpr":
		return "Super Ultra Mega Resort"
	case "sdm int 001":
		return "Stadium 1"
	case "sdm int 002":
		return "Stadium 2"
	case "isle of grom":
		return "Isle of Grom"
	}
	return name
}

func levelShortName(asset string) string {
	name := asset[strings.LastIndexAny(asset, `/\`)+1:]
	if len(name) >= 11 && strings.EqualFold(name[:11], "dingolevel_") {
		name = name[11:]
	}
	if len(name) >= 10 && strings.EqualFold(name[len(name)-10:], "_levelroot") {
		name = name[:len(name)-10]
	}
	return strings.ReplaceAll(name, "_", " ")
}

func foldPath(p string) string { return strings.ToLower(strings.ReplaceAll(p, `\`, "/")) }
