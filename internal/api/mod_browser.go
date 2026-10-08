package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/serverconfig"
	"github.com/xThrasherrr/ReSkateManager/internal/thunderstore"
)

// browseMod is a Thunderstore package as the mod browser shows it.
type browseMod struct {
	Name        string          `json:"name"`
	Owner       string          `json:"owner"`
	FullName    string          `json:"fullName"` // Namespace-Name
	URL         string          `json:"url"`
	Description string          `json:"description"`
	Icon        string          `json:"icon"`
	Website     string          `json:"website,omitempty"`
	Categories  []string        `json:"categories"`
	Rating      int             `json:"rating"`
	Downloads   int64           `json:"downloads"` // of every version
	Created     int64           `json:"created"`   // unix ms
	Updated     int64           `json:"updated"`
	Deprecated  bool            `json:"deprecated,omitempty"`
	NSFW        bool            `json:"nsfw,omitempty"`
	Versions    []browseVersion `json:"versions"` // newest first
	// The newest version's maps, once someone has looked inside it; null
	// until then.
	Maps        []serverconfig.ModMap `json:"maps"`
	MapsProblem string                `json:"mapsProblem,omitempty"`
}

type browseVersion struct {
	Version      string   `json:"version"`
	Size         int64    `json:"size"`
	Downloads    int64    `json:"downloads"`
	Created      int64    `json:"created"`
	Dependencies []string `json:"dependencies,omitempty"`
}

func unixMilli(t thunderstore.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// browseMods lists the community's packages on Thunderstore, newest first.
func (a *API) browseMods(w http.ResponseWriter, r *http.Request) {
	if a.Thunderstore == nil {
		writeErr(w, 404, "Thunderstore is turned off")
		return
	}
	pkgs, err := a.Thunderstore.Packages(r.Context(), r.URL.Query().Has("refresh"))
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Errorf("cannot reach Thunderstore: %w", err))
		return
	}
	out := make([]browseMod, 0, len(pkgs))
	for _, p := range pkgs {
		latest := p.Latest()
		if latest == nil {
			continue
		}
		m := browseMod{
			Name: p.Name, Owner: p.Owner, FullName: p.FullName, URL: p.URL,
			Description: latest.Description, Icon: latest.Icon, Website: latest.WebsiteURL,
			Categories: p.Categories, Rating: p.Rating, Created: unixMilli(p.Created), Updated: unixMilli(p.Updated),
			Deprecated: p.Deprecated, NSFW: p.NSFW, Versions: make([]browseVersion, len(p.Versions)),
		}
		if m.Categories == nil {
			m.Categories = []string{}
		}
		for i, v := range p.Versions {
			m.Downloads += v.Downloads
			m.Versions[i] = browseVersion{Version: v.Number, Size: v.FileSize, Downloads: v.Downloads, Created: unixMilli(v.Created), Dependencies: v.Dependencies}
		}
		if pr := a.knownMaps(latest); pr != nil {
			m.Maps, m.MapsProblem = pr.maps, pr.problem
		}
		out = append(out, m)
	}
	slices.SortFunc(out, func(x, y browseMod) int {
		if x.Updated != y.Updated {
			return cmp.Compare(y.Updated, x.Updated)
		}
		return strings.Compare(x.FullName, y.FullName)
	})
	writeJSON(w, 200, map[string]any{"mods": out, "fetched": a.Thunderstore.Fetched().UnixMilli()})
}

// A probe is a look inside one version's zip for the maps it adds. A
// version's zip never changes, so each answer is kept; a failure to reach
// Thunderstore is not.
type mapsProbe struct {
	ready   chan struct{}
	maps    []serverconfig.ModMap // nil when the zip cannot be looked inside
	problem string                // what is wrong with the zip, which installing it would hit too
	unknown string                // why the zip cannot be looked inside
	err     error
}

const (
	maxProbes  = 4096 // answers kept, beyond which they are all forgotten
	probesOnce = 4    // zips looked inside at a time
)

// knownMaps is version v's probe, if one has finished.
func (a *API) knownMaps(v *thunderstore.Version) *mapsProbe {
	a.probeMu.Lock()
	pr := a.probes[strings.ToLower(v.FullName)]
	a.probeMu.Unlock()
	if pr == nil {
		return nil
	}
	select {
	case <-pr.ready:
		if pr.err == nil {
			return pr
		}
	default:
	}
	return nil
}

// modMaps looks inside version v's zip for the maps it adds, or waits for a
// look already under way.
func (a *API) modMaps(ctx context.Context, v *thunderstore.Version) (*mapsProbe, error) {
	key := strings.ToLower(v.FullName)
	a.probeMu.Lock()
	if a.probes == nil || len(a.probes) >= maxProbes {
		a.probes = map[string]*mapsProbe{}
	}
	if a.probeSem == nil {
		a.probeSem = make(chan struct{}, probesOnce)
	}
	pr := a.probes[key]
	if pr == nil {
		pr = &mapsProbe{ready: make(chan struct{})}
		a.probes[key] = pr
		// Apart from the request, which may give up while others wait on it.
		a.goWork(func(ctx context.Context) { a.probe(ctx, pr, key, v) })
	}
	a.probeMu.Unlock()
	select {
	case <-pr.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if pr.err != nil {
		return nil, pr.err
	}
	return pr, nil
}

func (a *API) probe(ctx context.Context, pr *mapsProbe, key string, v *thunderstore.Version) {
	defer close(pr.ready)
	select {
	case a.probeSem <- struct{}{}:
	case <-ctx.Done():
		pr.err = ctx.Err()
		return
	}
	defer func() { <-a.probeSem }()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	err := func() (err error) {
		defer a.recovered("maps probe", &err)
		z, err := a.Thunderstore.OpenZip(ctx, v)
		switch {
		case errors.Is(err, thunderstore.ErrCannotLook):
			pr.unknown, err = err.Error(), nil
		case err == nil:
			pr.maps, err = serverconfig.ZipMaps(z.Reader)
			if err != nil && z.Err() == nil {
				pr.maps, pr.problem, err = []serverconfig.ModMap{}, err.Error(), nil
			}
		}
		return err
	}()
	if err != nil {
		pr.err = err
		a.probeMu.Lock()
		if a.probes[key] == pr {
			delete(a.probes, key) // the next look tries again
		}
		a.probeMu.Unlock()
	}
}

// browseModMaps answers the maps a version of a package adds, by looking
// inside its zip on Thunderstore: ?package=Namespace-Name&version=1.2.3, the
// newest version without one. maps is null, and unknown says why, for a zip
// that cannot be looked inside.
func (a *API) browseModMaps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	_, v, ok := a.findPackage(w, r, q.Get("package"), q.Get("version"))
	if !ok {
		return
	}
	pr, err := a.modMaps(r.Context(), v)
	if err != nil {
		a.fail(w, r, http.StatusBadGateway, fmt.Errorf("cannot look inside the mod: %w", err))
		return
	}
	writeJSON(w, 200, map[string]any{"maps": pr.maps, "problem": pr.problem, "unknown": pr.unknown})
}
