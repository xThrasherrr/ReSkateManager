package updater

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ManagerRelease is a newer manager release than the one running.
type ManagerRelease struct {
	Version string `json:"version"`
	URL     string `json:"url"` // the release page, where the archives are

	assets map[string]asset
}

// SelfChecker watches the manager's own GitHub releases. It reports a newer
// version; Install puts it in place when an owner asks, since swapping the
// manager means stopping every server it runs.
type SelfChecker struct {
	Repo    string // empty: never check
	Current string // the running version, as set at build time
	Client  *http.Client
	API     string // GitHub's API root; for tests
	Log     *slog.Logger

	mu     sync.Mutex
	latest *ManagerRelease
}

// Newer returns the newest release if it is newer than the running manager.
func (s *SelfChecker) Newer() *ManagerRelease {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// Run checks now and every six hours until ctx ends.
func (s *SelfChecker) Run(ctx context.Context) {
	if s == nil || s.Repo == "" || parseVersion(s.Current) == nil {
		return // dev builds have no version to compare
	}
	for {
		if err := s.Check(ctx); err != nil {
			s.Log.Debug("manager update check", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// githubRelease is a release as GitHub's API gives it.
type githubRelease struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Draft   bool    `json:"draft"`
	Assets  []asset `json:"assets"`
}

// Check looks for a newer manager release now. A release follows releases
// only, which GitHub's latest is. A release candidate also follows the
// candidates after it, so a trial install reaches the release it was trying.
func (s *SelfChecker) Check(ctx context.Context) error {
	cur := parseVersion(s.Current)
	candidate := cur != nil && cur[3] != final
	var rels []githubRelease
	if candidate {
		if err := s.get(ctx, "/releases?per_page=30", &rels); err != nil {
			return err
		}
	} else {
		var rel githubRelease
		if err := s.get(ctx, "/releases/latest", &rel); err != nil {
			return err
		}
		rels = []githubRelease{rel}
	}
	var newer *ManagerRelease
	best := cur
	for _, rel := range rels {
		v := parseVersion(rel.TagName)
		if cur == nil || v == nil || rel.Draft || (!candidate && v[3] != final) || !versionLess(best, v) {
			continue
		}
		best = v
		newer = &ManagerRelease{Version: strings.TrimPrefix(rel.TagName, "v"), URL: rel.HTMLURL, assets: map[string]asset{}}
		for _, a := range rel.Assets {
			newer.assets[a.Name] = a
		}
	}
	s.mu.Lock()
	s.latest = newer
	s.mu.Unlock()
	return nil
}

// get decodes the repository's path from GitHub's API into v, which stays
// empty when there is nothing there: nothing published yet.
func (s *SelfChecker) get(ctx context.Context, path string, v any) error {
	api := s.API
	if api == "" {
		api = "https://api.github.com"
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+s.Repo+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("GitHub replied " + resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// versionRe is a version as tagged: three numbers, no sign and no leading
// zeros, as semver writes them, and "-rc.N" on a release candidate.
var versionRe = regexp.MustCompile(`^v?(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})(?:-rc\.(0|[1-9]\d{0,8}))?$`)

// final is a release's place after its candidates, as parseVersion gives it.
const final = math.MaxInt32

// parseVersion reads "v1.2.3", "1.2.3" or "1.2.3-rc.4" as major, minor,
// patch and the candidate's number (final for a release); anything else (dev,
// snapshots, other pre-releases) is nil.
func parseVersion(v string) []int {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return nil
	}
	out := make([]int, 4)
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	if m[4] == "" {
		out[3] = final
	}
	return out
}

func versionLess(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
