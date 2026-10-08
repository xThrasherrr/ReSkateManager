package serverconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// WorldLayer is one switchable part of a map, from world-layers.json (which
// players' games write and servers ship next to the executable).
type WorldLayer struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	Map      string `json:"map"`
}

// TimesOfDay are the tod command's choices; index n matches "_tod_<n>_" in a
// layer key, and 0 means each map's own lighting.
var TimesOfDay = []string{"default", "morning", "noon", "afternoon", "evening", "night", "weatherday", "weathernight"}

// LayerModes are what a world layer can be set to.
var LayerModes = []string{"default", "on", "off"}

// ReadWorldLayers lists the layers in dir/world-layers.json. A missing file is
// no layers, not an error: the server then refuses the layer commands too.
func ReadWorldLayers(dir string) ([]WorldLayer, error) {
	data, err := os.ReadFile(filepath.Join(dir, "world-layers.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Rows []WorldLayer `json:"rows"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("world-layers.json is not valid: %w", err)
	}
	// A key is one setting; listed twice, the first counts.
	seen := map[string]bool{}
	return slices.DeleteFunc(file.Rows, func(l WorldLayer) bool {
		dup := l.Key == "" || seen[l.Key]
		seen[l.Key] = true
		return dup
	}), nil
}

// Layers is the world layers set in the config, by key.
func (f *File) Layers() map[string]string {
	out := map[string]string{}
	m, _ := f.Root["layers"].(map[string]any)
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// SetLayers replaces the world layers set in the config.
func (f *File) SetLayers(layers map[string]string) {
	m := make(map[string]any, len(layers))
	for k, v := range layers {
		m[k] = v
	}
	f.Root["layers"] = m
}

// todSlot is the n in a "<map>_tod_<n>_<name>" key, or -1.
func todSlot(key string) int {
	at := strings.Index(key, "_tod_")
	if at < 0 || at+5 >= len(key) || key[at+5] < '1' || key[at+5] > '7' {
		return -1
	}
	return int(key[at+5] - '0')
}

// ApplyTimeOfDay does what the server's tod command does to the layers: one
// time on and the rest off on every map, or all back to the map's own.
func ApplyTimeOfDay(layers map[string]string, all []WorldLayer, tod string) error {
	slot := slices.Index(TimesOfDay, tod)
	if slot < 0 {
		return fmt.Errorf("unknown time of day %q", tod)
	}
	n := 0
	for _, l := range all {
		s := todSlot(l.Key)
		if s < 0 {
			continue
		}
		n++
		switch {
		case slot == 0:
			delete(layers, l.Key)
		case s == slot:
			layers[l.Key] = "on"
		default:
			layers[l.Key] = "off"
		}
	}
	if n == 0 {
		return errors.New("world-layers.json has no time-of-day layers")
	}
	return nil
}

// TimeOfDay reads the time of day back from the layers: one of TimesOfDay, or
// "custom" when the time layers were set one by one.
func TimeOfDay(layers map[string]string, all []WorldLayer) string {
	for slot, name := range TimesOfDay {
		match, found := true, false
		for _, l := range all {
			s := todSlot(l.Key)
			if s < 0 {
				continue
			}
			found = true
			want := ""
			if slot != 0 {
				want = map[bool]string{true: "on", false: "off"}[s == slot]
			}
			if layers[l.Key] != want {
				match = false
				break
			}
		}
		if !found {
			return ""
		}
		if match {
			return name
		}
	}
	return "custom"
}
