// Package updater installs ReSkateServer releases the way the server's own
// updater does (Server/server_update.cpp, Launcher/updater.cpp): the newest
// GitHub release carries launcher.json, whose "server" block pins the server
// zip by SHA-256 and size, and the exe inside it by exe_sha256. A server is out
// of date when its exe's SHA-256 differs from exe_sha256.
//
// launcher.json pins only the Windows zip. On Linux the release's
// ReSkateServer-Linux-<version>.tar.gz is pinned by the SHA-256 digest GitHub
// records for the asset, and the exe's SHA-256 is read from the tarball itself.
package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxDownload = 512 << 20

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ExeName is the server binary inside a release for this platform.
func ExeName() string {
	if runtime.GOOS == "windows" {
		return "ReSkateServer.exe"
	}
	return "ReSkateServer"
}

// Supported reports whether releases carry a server for this platform.
func Supported() bool { return runtime.GOOS == "windows" || runtime.GOOS == "linux" }

// Release is a server release for this platform: where to download it, and
// the sums to check the download by.
type Release struct {
	Tag       string `json:"tag"`
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	ExeSHA256 string `json:"exeSha256"`
	CheckedAt int64  `json:"checkedAt"`
	// Archive is "zip" (Windows) or "tar.gz" (Linux).
	Archive string `json:"-"`
}

// Checker finds the newest server release, from GitHub or a launcher.json.
type Checker struct {
	Repo   string
	Client *http.Client
	// LauncherJSON overrides the release lookup (a local file or URL), for testing a release before publishing it.
	LauncherJSON string
	// GOOS picks the platform's server; empty means this machine's. API is GitHub's API root. Both are for tests.
	GOOS string
	API  string
	// Learn hears of each release Latest finds, so its program can be told
	// apart later (Builds.Learn).
	Learn func(*Release)

	mu     sync.Mutex
	cached *Release
	err    error
	at     time.Time
	// exeSums remembers the exe SHA-256 read from each Linux tarball, by the tarball's SHA-256.
	exeSums map[string]string
}

// NewChecker looks for releases of the GitHub repository repo (owner/name).
func NewChecker(repo string) *Checker {
	return &Checker{Repo: repo, Client: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Checker) goos() string {
	if c.GOOS != "" {
		return c.GOOS
	}
	return runtime.GOOS
}

// Latest returns the newest release, cached for ten minutes (GitHub's API is rate limited).
func (c *Checker) Latest(ctx context.Context, force bool) (*Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && time.Since(c.at) < 10*time.Minute && (c.cached != nil || c.err != nil) {
		return c.cached, c.err
	}
	c.cached, c.err = c.fetch(ctx)
	c.at = time.Now()
	if c.cached != nil && c.Learn != nil {
		c.Learn(c.cached)
	}
	return c.cached, c.err
}

func (c *Checker) open(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ReSkateManager")
	if strings.Contains(url, "api.github.com/") || (c.API != "" && strings.HasPrefix(url, c.API)) {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

func (c *Checker) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := c.open(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

type launcherJSON struct {
	Schema int `json:"schema"`
	Server *struct {
		Version   string `json:"version"`
		URL       string `json:"url"`
		SHA256    string `json:"sha256"`
		Size      int64  `json:"size"`
		ExeSHA256 string `json:"exe_sha256"`
	} `json:"server"`
}

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // "sha256:<hex>", recorded by GitHub at upload
}

func (c *Checker) latestRelease(ctx context.Context) (string, map[string]asset, error) {
	api := c.API
	if api == "" {
		api = "https://api.github.com"
	}
	body, err := c.get(ctx, api+"/repos/"+c.Repo+"/releases/latest", 4<<20)
	if err != nil {
		return "", nil, fmt.Errorf("GitHub could not be reached: %w", err)
	}
	var rel struct {
		TagName string  `json:"tag_name"`
		Assets  []asset `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", nil, fmt.Errorf("unexpected GitHub reply: %w", err)
	}
	assets := map[string]asset{}
	for _, a := range rel.Assets {
		assets[a.Name] = a
	}
	return rel.TagName, assets, nil
}

func (c *Checker) fetch(ctx context.Context) (*Release, error) {
	if c.goos() == "linux" {
		if c.LauncherJSON != "" {
			return nil, errors.New("a launcher.json override pins only a Windows server")
		}
		return c.fetchLinux(ctx)
	}
	assets := map[string]asset{}
	tag := ""
	var manifest []byte
	var err error
	switch {
	case c.LauncherJSON != "" && !isURL(c.LauncherJSON):
		manifest, err = os.ReadFile(c.LauncherJSON)
	case c.LauncherJSON != "":
		manifest, err = c.get(ctx, c.LauncherJSON, 1<<20)
	default:
		tag, assets, err = c.latestRelease(ctx)
		if err != nil {
			return nil, err
		}
		a, ok := assets["launcher.json"]
		if !ok {
			return nil, errors.New("the latest release has no launcher.json")
		}
		manifest, err = c.get(ctx, a.URL, 1<<20)
	}
	if err != nil {
		return nil, err
	}
	var lj launcherJSON
	if err := json.Unmarshal(manifest, &lj); err != nil {
		return nil, fmt.Errorf("launcher.json: %w", err)
	}
	if lj.Schema != 1 {
		return nil, fmt.Errorf("launcher.json schema %d is not supported; update the manager", lj.Schema)
	}
	s := lj.Server
	if s == nil || s.URL == "" {
		return nil, errors.New("the latest release has no server")
	}
	if !sha256Re.MatchString(s.SHA256) || !sha256Re.MatchString(s.ExeSHA256) || s.Size <= 0 || s.Size > maxDownload {
		return nil, errors.New("launcher.json has a malformed server entry")
	}
	r := &Release{Tag: tag, Version: s.Version, URL: s.URL, SHA256: s.SHA256, Size: s.Size, ExeSHA256: s.ExeSHA256,
		CheckedAt: time.Now().UnixMilli(), Archive: "zip"}
	if name, ok := strings.CutPrefix(s.URL, "asset:"); ok {
		a, found := assets[name]
		if !found {
			return nil, fmt.Errorf("the latest release is missing asset %s", name)
		}
		r.URL = a.URL
	} else if !strings.HasPrefix(s.URL, "https://") {
		return nil, errors.New("launcher.json server url must be HTTPS")
	}
	return r, nil
}

const linuxPrefix, linuxSuffix = "ReSkateServer-Linux-", ".tar.gz"

func (c *Checker) fetchLinux(ctx context.Context) (*Release, error) {
	tag, assets, err := c.latestRelease(ctx)
	if err != nil {
		return nil, err
	}
	a, err := linuxAsset(assets)
	if err != nil {
		return nil, err
	}
	digest, ok := strings.CutPrefix(a.Digest, "sha256:")
	if !ok || !sha256Re.MatchString(digest) || a.Size <= 0 || a.Size > maxDownload {
		return nil, errors.New("GitHub gives no SHA-256 for the release's Linux server")
	}
	r := &Release{Tag: tag, Version: strings.TrimSuffix(strings.TrimPrefix(a.Name, linuxPrefix), linuxSuffix), URL: a.URL,
		SHA256: digest, Size: a.Size, CheckedAt: time.Now().UnixMilli(), Archive: "tar.gz"}
	if sum, ok := c.exeSums[digest]; ok {
		r.ExeSHA256 = sum
		return r, nil
	}
	// The exe is the tarball's first file, so this reads only its first megabyte or two.
	resp, err := c.open(ctx, a.URL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r.ExeSHA256, err = exeSHA256(io.LimitReader(resp.Body, a.Size), "tar.gz")
	if err != nil {
		return nil, fmt.Errorf("read the Linux server from %s: %w", a.Name, err)
	}
	if c.exeSums == nil {
		c.exeSums = map[string]string{}
	}
	c.exeSums[digest] = r.ExeSHA256
	return r, nil
}

// FileSHA256 hashes a file; "" when it does not exist.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Download fetches the release archive into cacheDir (reusing a verified copy)
// and checks the archive and the exe inside it.
func (c *Checker) Download(ctx context.Context, r *Release, cacheDir string, progress func(done, total int64)) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	archive := r.Archive
	if archive == "" {
		archive = "zip"
	}
	target := filepath.Join(cacheDir, "ReSkateServer-"+r.SHA256[:16]+"."+archive)
	if sum, _ := FileSHA256(target); sum == r.SHA256 {
		now := time.Now()
		_ = os.Chtimes(target, now, now) // just used, so PruneCache keeps it
		return target, verifyExe(target, r.ExeSHA256)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ReSkateManager")
	client := &http.Client{} // no overall timeout: the archive is large; ctx bounds it
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	// A file of its own: two servers updating at once download alike.
	f, err := os.CreateTemp(cacheDir, filepath.Base(target)+".*.part")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	body := io.LimitReader(resp.Body, r.Size+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				os.Remove(tmp)
				return "", err
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done, r.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return "", rerr
		}
	}
	// A write that failed late (a full disk) can surface only here.
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if done != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		os.Remove(tmp)
		return "", errors.New("the download does not match the release (size or SHA-256)")
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return target, verifyExe(target, r.ExeSHA256)
}

// linuxAsset picks the release's Linux server: the one, or of several (more
// architectures, say), the one for this machine's.
func linuxAsset(assets map[string]asset) (asset, error) {
	var found []asset
	for name, a := range assets {
		if strings.HasPrefix(name, linuxPrefix) && strings.HasSuffix(name, linuxSuffix) {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 0:
		return asset{}, errors.New("the latest release has no Linux server")
	case 1:
		return found[0], nil
	}
	slices.SortFunc(found, func(x, y asset) int { return strings.Compare(x.Name, y.Name) })
	for _, a := range found {
		lower := strings.ToLower(a.Name)
		if strings.Contains(lower, runtime.GOARCH) || runtime.GOARCH == "amd64" && (strings.Contains(lower, "x64") || strings.Contains(lower, "x86_64")) {
			return a, nil
		}
	}
	return asset{}, fmt.Errorf("the latest release has %d Linux servers and none says it is for %s", len(found), runtime.GOARCH)
}

// isURL reports whether s is a web address rather than a file's path.
func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// CheckOverride checks an address standing in for GitHub, or a launcher.json
// to install from: HTTPS, or plain HTTP to this machine only. Over plain HTTP
// to anywhere else, whoever sits on the network picks the server the manager
// installs, hashes and all.
func CheckOverride(raw string) error {
	if !isURL(raw) {
		return fmt.Errorf("%q is not a web address", raw)
	}
	u, _ := url.Parse(raw)
	host := u.Hostname()
	if u.Scheme == "https" || host == "localhost" || net.ParseIP(host).IsLoopback() {
		return nil
	}
	return fmt.Errorf("%q must be https:// (or http:// to this machine)", raw)
}

func archiveKind(p string) string {
	if strings.HasSuffix(p, ".tar.gz") {
		return "tar.gz"
	}
	return "zip"
}

func verifyExe(archivePath, want string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	got, err := exeSHA256(f, archiveKind(archivePath))
	if err != nil {
		return err
	}
	if got != want {
		return errors.New("the release's server exe does not match the release manifest")
	}
	return nil
}

var errFound = errors.New("found")

// exeSHA256 hashes the server exe in an archive, stopping as soon as it is read.
func exeSHA256(r io.Reader, kind string) (string, error) {
	var sum string
	err := walk(r, kind, func(name string, _ os.FileMode, body io.Reader) error {
		if name != "ReSkateServer.exe" && name != "ReSkateServer" {
			return nil
		}
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(body, maxDownload)); err != nil {
			return err
		}
		sum = hex.EncodeToString(h.Sum(nil))
		return errFound
	})
	if errors.Is(err, errFound) {
		return sum, nil
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("the release archive has no server exe")
}

// walk calls fn for every regular file in a zip or tar.gz, in archive order,
// with the archive's single top-level folder (the tarball's) removed from names.
// Links and other special entries are refused.
func walk(r io.Reader, kind string, fn func(name string, mode os.FileMode, body io.Reader) error) error {
	if kind == "zip" {
		f, ok := r.(*os.File)
		if !ok {
			return errors.New("a zip must be read from a file")
		}
		st, err := f.Stat()
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(f, st.Size())
		if err != nil {
			return err
		}
		for _, zf := range zr.File {
			// Zips made on Windows may part folders with backslashes, as
			// ReSkate 1.1.8's does. Install still refuses a name that leaves
			// the server's folder.
			name := strings.ReplaceAll(zf.Name, `\`, "/")
			if zf.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
				continue
			}
			if !zf.Mode().IsRegular() {
				return fmt.Errorf("the update contains a link or special file: %s", zf.Name)
			}
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			// Cleaned as the tarball's names are, so "./ReSkateServer.json"
			// is still known for the host's own file.
			err = fn(path.Clean(name), zf.Mode(), rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	root := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			// Release tarballs wrap everything in one ReSkateServer-Linux-<version>/ folder, their first entry.
			if root == "" && strings.HasPrefix(name, "ReSkateServer") && strings.Count(strings.TrimSuffix(name, "/"), "/") == 0 {
				root = strings.TrimSuffix(name, "/") + "/"
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("the update contains a link or special file: %s", h.Name)
		}
		if root != "" {
			rest, ok := strings.CutPrefix(name, root)
			if !ok {
				return fmt.Errorf("the update has a file outside %s: %s", root, h.Name)
			}
			name = rest
		}
		if err := fn(path.Clean(name), h.FileInfo().Mode(), tr); err != nil {
			return err
		}
	}
}

// Protected files are the host's own: a release creates them when missing but never replaces them.
func protected(rel string) bool {
	switch strings.ToLower(filepath.ToSlash(rel)) {
	case "reskateserver.json", "world-layers.json", "data/bans.json":
		return true
	}
	return false
}

// skipped files are never written by a release.
func skipped(rel string) bool {
	rel = strings.ToLower(filepath.ToSlash(rel))
	return strings.HasPrefix(rel, "mods/") || strings.HasSuffix(rel, ".log")
}

// Install extracts the archive over dir. The server must be stopped. Every
// file is staged first, so a failed extract leaves the install untouched.
func Install(archivePath, dir string) ([]string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	type staged struct{ target, tmp, old, name string }
	var files []staged
	cleanup := func() {
		for _, s := range files {
			os.Remove(s.tmp)
		}
	}
	err = walk(f, archiveKind(archivePath), func(name string, mode os.FileMode, body io.Reader) error {
		local := filepath.FromSlash(name)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("the update contains an unsafe path: %s", name)
		}
		target := filepath.Join(root, local)
		if skipped(name) {
			return nil
		}
		if protected(name) {
			if _, err := os.Stat(target); err == nil {
				return nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		s := staged{target: target, tmp: target + ".update-new", name: name}
		if err := extract(name, mode, body, s.tmp); err != nil {
			os.Remove(s.tmp)
			return err
		}
		files = append(files, s)
		return nil
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	// Each file the release replaces is set aside first, and put back if a
	// later one can't go in (an antivirus holding the exe, say): a server is
	// either the old release or the new, never half of each.
	var moved []staged
	undo := func() error {
		var errs []error
		for _, s := range slices.Backward(moved) {
			if s.old != "" {
				errs = append(errs, os.Rename(s.old, s.target))
			} else {
				errs = append(errs, os.Remove(s.target))
			}
		}
		cleanup()
		return errors.Join(errs...)
	}
	for _, s := range files {
		if _, err := os.Lstat(s.target); err == nil {
			s.old = s.target + ".update-old"
			os.Remove(s.old)
			if err := os.Rename(s.target, s.old); err != nil {
				return nil, errors.Join(fmt.Errorf("cannot replace %s: %w", s.name, err), undo())
			}
		}
		if err := os.Rename(s.tmp, s.target); err != nil {
			if s.old != "" {
				err = errors.Join(err, os.Rename(s.old, s.target))
			}
			return nil, errors.Join(fmt.Errorf("cannot replace %s: %w", s.name, err), undo())
		}
		moved = append(moved, s)
	}
	installed := make([]string, len(moved))
	for i, s := range moved {
		if s.old != "" {
			os.Remove(s.old)
		}
		installed[i] = s.name
	}
	return installed, nil
}

func extract(name string, mode os.FileMode, body io.Reader, to string) error {
	perm := os.FileMode(0o644)
	// Zips made on Windows carry no Unix modes, so the server is marked executable by name too.
	if mode&0o111 != 0 || strings.EqualFold(path.Base(name), "ReSkateServer") || strings.HasSuffix(name, ".so") {
		perm = 0o755
	}
	os.Remove(to) // a leftover from an interrupted update would keep its old mode
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(body, maxDownload)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
