package serverconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A park mod's files (ReSkate 2.0.6): parks/<key>.park.json, the parks the
// server spawns, and manifest.json, which names the mod. The Parks page
// shows what the parks hold and edits the files as text.

// maxParkFile is decode_park's cap (Extension/Objects/ParkEditor/park_document.cpp).
const maxParkFile = 2 << 20

const parkSuffix = ".park.json"

// parkKeys lists the parks of the mod at path, by the key of each
// parks/<key>.park.json.
func parkKeys(path string) []string {
	entries, err := os.ReadDir(filepath.Join(path, "parks"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if key, ok := strings.CutSuffix(e.Name(), parkSuffix); ok && key != "" && !e.IsDir() {
			out = append(out, key)
		}
	}
	return out
}

// Park is one of a mod's parks, as the Parks page shows it.
type Park struct {
	Folder  string `json:"folder"`
	Key     string `json:"key"`
	Objects int    `json:"objects"`           // the ones the server spawns
	Skipped int    `json:"skipped,omitempty"` // the ones it leaves out, which players' games can't show
	// Box holds its objects' positions: the lowest x, y and z, then the
	// highest, to tell parks that would overlap.
	Box     [6]float64 `json:"box"`
	Problem string     `json:"problem,omitempty"` // why the server would refuse it
}

// ReadParks reads the parks of the mods the server loads.
func ReadParks(dir string) ([]Park, error) {
	mods, err := ReadMods(dir)
	if err != nil {
		return nil, err
	}
	out := []Park{}
	for _, m := range mods {
		if m.Disabled {
			continue
		}
		for _, key := range m.Parks {
			p := Park{Folder: m.Folder, Key: key}
			data, err := readCapped(filepath.Join(ModsDir(dir), m.Folder, "parks", key+parkSuffix), maxParkFile)
			if err == nil {
				p.Objects, p.Skipped, p.Box, err = checkPark(data, key)
			}
			if err != nil {
				p.Problem = err.Error()
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// maxParkObjects is max_owned_objects (Extension/Multiplayer/Session/object_state.h).
const maxParkObjects = 1024

// checkPark reads the park for key as the server's load_park_mod does
// (Server/server_parks.cpp): it refuses a file that isn't a ReSkate park or
// has no object players' games can show, and skips each object that isn't
// one, counted in skipped.
func checkPark(data []byte, key string) (objects, skipped int, box [6]float64, err error) {
	var root map[string]any
	if err := decodeJSON(bytes.TrimPrefix(data, utf8BOM)).Decode(&root); err != nil {
		return 0, 0, box, fmt.Errorf("it isn't valid JSON (%w)", err)
	}
	if root["format"] != "reskate-park" {
		return 0, 0, box, errors.New(`it isn't a ReSkate park: "format" must be "reskate-park"`)
	}
	if v, ok := root["schema_version"]; ok {
		n, _ := v.(json.Number)
		if f, err := n.Float64(); err != nil || f != 1 {
			return 0, 0, box, errors.New("it's from a newer ReSkate; update the server")
		}
	}
	maps, _ := root["maps"].(map[string]any)
	list, ok := maps[key]
	if !ok {
		return 0, 0, box, fmt.Errorf(`it has no objects for %s under "maps"`, key)
	}
	entries, ok := list.([]any)
	if !ok {
		return 0, 0, box, fmt.Errorf("it isn't a ReSkate park: the objects for %s aren't a list", key)
	}
	if len(entries) > maxParkObjects {
		return 0, 0, box, fmt.Errorf("it has %d objects; at most %d can be shown", len(entries), maxParkObjects)
	}
	scales, _ := root["object_scales"].(map[string]any)
	ofKey, _ := scales[key].(map[string]any)
	for _, e := range entries {
		pos, ok := parkObject(e, ofKey)
		if !ok {
			skipped++
			continue
		}
		for a, x := range pos {
			if objects == 0 || x < box[a] {
				box[a] = x
			}
			if objects == 0 || x > box[a+3] {
				box[a+3] = x
			}
		}
		objects++
	}
	if objects == 0 {
		if len(entries) == 0 {
			return 0, 0, box, errors.New("it has no objects")
		}
		return 0, skipped, box, errors.New("it has no objects players' games can show (Build Kit items)")
	}
	return objects, skipped, box, nil
}

var buildKitItem = regexp.MustCompile(`^own_bk[A-Za-z0-9_]*$`)

// parkObject is an object's position when every player's game would show it
// (valid_network_object in Extension/Multiplayer/Session/object_state.h): a
// Build Kit item, a place on the map, a real rotation, a size it can show.
func parkObject(v any, scales map[string]any) ([]float64, bool) {
	o, _ := v.(map[string]any)
	item, _ := o["item"].(string)
	pos, rot := numbers(o["position"]), numbers(o["rotation"])
	if len(item) > 256 || !buildKitItem.MatchString(item) || len(pos) != 3 || len(rot) != 4 {
		return nil, false
	}
	for _, x := range pos {
		if math.Abs(x) > 100000 {
			return nil, false
		}
	}
	norm := 0.0
	for _, x := range rot {
		norm += x * x
	}
	if norm <= .98 || norm >= 1.02 {
		return nil, false
	}
	// A size other than 1 is "object_scales": {key: {id: scale}}.
	if id, ok := o["id"].(json.Number); ok && scales != nil {
		f, _ := id.Float64()
		if s, ok := scales[strconv.FormatUint(uint64(f), 10)]; ok {
			n, ok := s.(json.Number)
			scale, err := n.Float64()
			if !ok || err != nil || scale < .01 || scale > 100 {
				return nil, false
			}
		}
	}
	return pos, true
}

// numbers is a JSON list of finite numbers, nil when it's anything else.
func numbers(v any) []float64 {
	list, _ := v.([]any)
	out := make([]float64, 0, len(list))
	for _, x := range list {
		n, ok := x.(json.Number)
		if !ok {
			return nil
		}
		f, err := n.Float64()
		if err != nil || math.IsInf(f, 0) {
			return nil
		}
		out = append(out, f)
	}
	return out
}

// ErrBadModFile is a mod file the park editor won't read or write.
var ErrBadModFile = errors.New("can't edit that file")

// ErrModFileChanged is a save over a file that changed since it was read.
var ErrModFileChanged = errors.New("it changed since it was opened")

// modFileRe is what the park editor edits: a mod's manifest.json and parks.
var modFileRe = regexp.MustCompile(`^(?:manifest\.json|parks/[A-Za-z0-9_]{1,32}\.park\.json)$`)

// openModFile opens the folder of the server's own mod, for rel in it.
// A link to the shared mods is every server's, so no one server edits it.
func openModFile(dir, folder, rel string) (*os.Root, error) {
	if !modFileRe.MatchString(rel) {
		return nil, fmt.Errorf("%w: only manifest.json and parks/<key>.park.json", ErrBadModFile)
	}
	p, _, err := findMod(dir, folder)
	if err != nil {
		return nil, err
	}
	if linkTarget(p) != "" {
		return nil, fmt.Errorf("%w: it's linked from the shared mods, which every server using them reads", ErrBadModFile)
	}
	return os.OpenRoot(p)
}

// ReadModFile is a park mod's file as text, and the hash a save of it gives back.
func ReadModFile(dir, folder, rel string) (text, hash string, err error) {
	root, err := openModFile(dir, folder, rel)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	data, err := readRootCapped(root, rel)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(data) {
		return "", "", fmt.Errorf("%w: %s isn't text", ErrBadModFile, rel)
	}
	return string(data), modFileSum(data), nil
}

// WriteModFile replaces a park mod's file with text, checked as the server
// reads it. was is the hash ReadModFile gave; when the file no longer has
// it, nothing is written, and ErrModFileChanged comes with the hash it has.
func WriteModFile(dir, folder, rel, text, was string) (hash string, err error) {
	if len(text) > maxParkFile {
		return "", fmt.Errorf("%w: %s would be over 2 MB", ErrBadModFile, rel)
	}
	data := []byte(text)
	if rel == "manifest.json" {
		var m map[string]any
		if err := json.Unmarshal(bytes.TrimPrefix(data, utf8BOM), &m); err != nil || m == nil {
			return "", fmt.Errorf("%w: manifest.json must be a JSON object", ErrBadModFile)
		}
	} else if _, _, _, err := checkPark(data, strings.TrimSuffix(strings.TrimPrefix(rel, "parks/"), parkSuffix)); err != nil {
		return "", fmt.Errorf("%w: the server would refuse this park: %v", ErrBadModFile, err)
	}
	root, err := openModFile(dir, folder, rel)
	if err != nil {
		return "", err
	}
	defer root.Close()
	old, err := readRootCapped(root, rel)
	if err != nil {
		return "", err
	}
	if cur := modFileSum(old); cur != was {
		return cur, ErrModFileChanged
	}
	// Whole or not at all: the server reads it again on every park-mod add.
	tmp := path.Join(path.Dir(rel), "."+path.Base(rel)+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", err
	}
	defer root.Remove(tmp) // gone already, once renamed
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		return "", err
	}
	return modFileSum(data), nil
}

// readRootCapped reads an existing regular file in root, refusing one over
// a park's cap.
func readRootCapped(root *os.Root, rel string) ([]byte, error) {
	st, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: the mod has no %s", ErrBadModFile, rel)
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s isn't a file", ErrBadModFile, rel)
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxParkFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxParkFile {
		return nil, fmt.Errorf("%w: %s is over 2 MB", ErrBadModFile, rel)
	}
	return data, nil
}

func modFileSum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}
