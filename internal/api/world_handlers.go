package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
)

func (a *API) world(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	all, err := serverconfig.ReadWorldLayers(in.Def().Dir)
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	f, err := serverconfig.Read(in.Def().ConfigPath())
	if err != nil {
		writeErr(w, 404, "the server has no config yet; start it once")
		return
	}
	layers := f.Layers()
	layerSync, _ := f.Root["world_layer_sync"].(bool)
	if all == nil {
		all = []serverconfig.WorldLayer{}
	}
	writeJSON(w, 200, map[string]any{
		"layers":    all,
		"values":    layers,
		"timeOfDay": serverconfig.TimeOfDay(layers, all),
		"sync":      layerSync,
	})
}

// saveWorld sets the time of day and then single layers ("default" clears
// one), live through the console or in the config of a stopped server.
func (a *API) saveWorld(w http.ResponseWriter, r *http.Request) {
	in := inst(r)
	var req struct {
		TimeOfDay string            `json:"timeOfDay"`
		Values    map[string]string `json:"values"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	all, err := serverconfig.ReadWorldLayers(in.Def().Dir)
	if err != nil {
		a.fail(w, r, 400, err)
		return
	}
	if len(all) == 0 {
		writeErr(w, 400, "world layers need world-layers.json next to the server")
		return
	}
	if req.TimeOfDay != "" && !slices.Contains(serverconfig.TimesOfDay, req.TimeOfDay) {
		writeErr(w, 400, "unknown time of day")
		return
	}
	keys := make([]string, 0, len(req.Values))
	for k, mode := range req.Values {
		if !slices.ContainsFunc(all, func(l serverconfig.WorldLayer) bool { return l.Key == k }) {
			writeErr(w, 400, fmt.Sprintf("no world layer is called %q", k))
			return
		}
		if !slices.Contains(serverconfig.LayerModes, mode) {
			writeErr(w, 400, "layers are default, on or off")
			return
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if req.TimeOfDay == "" && len(keys) == 0 {
		writeErr(w, 400, "nothing to change")
		return
	}
	summary := worldSummary(req.TimeOfDay, keys, req.Values)

	if running(in) {
		var cmds []string
		if req.TimeOfDay != "" {
			cmds = append(cmds, "tod "+req.TimeOfDay)
		}
		// The server takes several key=mode pairs per command; keep lines short.
		for start := 0; start < len(keys); start += 20 {
			var pairs []string
			for _, k := range keys[start:min(start+20, len(keys))] {
				pairs = append(pairs, k+"="+req.Values[k])
			}
			cmds = append(cmds, "layers "+strings.Join(pairs, " "))
		}
		var replies []string
		var err error
		for i, cmd := range cmds {
			var reply string
			if reply, err = in.Command(r.Context(), cmd, from(r).user.Username); err != nil {
				if i > 0 {
					// What went before took: say so, or the page reads as nothing changed.
					err = fmt.Errorf("%s Then: %w", strings.Join(replies, " "), err)
				}
				break
			}
			replies = append(replies, reply)
		}
		a.auditOutcome(r, in.ID(), "world.update", summary, err)
		a.answerReply(w, r, strings.Join(replies, " "), err)
		return
	}
	reply, err := a.offlineEdit(in, func(f *serverconfig.File) (string, error) {
		layers := f.Layers()
		if req.TimeOfDay != "" {
			if err := serverconfig.ApplyTimeOfDay(layers, all, req.TimeOfDay); err != nil {
				return "", err
			}
		}
		for _, k := range keys {
			if mode := req.Values[k]; mode == "default" {
				delete(layers, k)
			} else {
				layers[k] = mode
			}
		}
		f.SetLayers(layers)
		return "Saved; the server uses these world layers when it starts.", nil
	})
	a.auditOutcome(r, in.ID(), "world.update", summary, err)
	a.answerReply(w, r, reply, err)
}

func worldSummary(tod string, keys []string, values map[string]string) string {
	var parts []string
	if tod != "" {
		parts = append(parts, "time of day "+tod)
	}
	for _, k := range keys {
		parts = append(parts, k+"="+values[k])
	}
	return strings.Join(parts, ", ")
}
