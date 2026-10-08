// Package thunderstore looks up ReSkate mods on Thunderstore, where most of
// them are published, so the panel can browse them, install them and tell
// when an installed one has a newer version.
package thunderstore

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// Version is one release of a package, as Thunderstore lists it.
type Version struct {
	FullName     string   `json:"full_name"` // Namespace-Name-1.2.3
	Description  string   `json:"description"`
	Icon         string   `json:"icon"`
	Number       string   `json:"version_number"`
	Dependencies []string `json:"dependencies"` // Namespace-Name-1.2.3
	DownloadURL  string   `json:"download_url"`
	Downloads    int64    `json:"downloads"`
	Created      Time     `json:"date_created"`
	WebsiteURL   string   `json:"website_url"`
	FileSize     int64    `json:"file_size"`
}

// Package is a mod on Thunderstore, with its versions.
type Package struct {
	Name       string    `json:"name"`
	Owner      string    `json:"owner"`
	FullName   string    `json:"full_name"` // Namespace-Name
	URL        string    `json:"package_url"`
	Created    Time      `json:"date_created"`
	Updated    Time      `json:"date_updated"`
	Rating     int       `json:"rating_score"`
	Pinned     bool      `json:"is_pinned"`
	Deprecated bool      `json:"is_deprecated"`
	NSFW       bool      `json:"has_nsfw_content"`
	Categories []string  `json:"categories"`
	Versions   []Version `json:"versions"` // newest first
}

// Latest is the package's newest version.
func (p *Package) Latest() *Version {
	if len(p.Versions) == 0 {
		return nil
	}
	return &p.Versions[0]
}

// Version finds one of the package's versions by number.
func (p *Package) Version(number string) *Version {
	for i := range p.Versions {
		if p.Versions[i].Number == number {
			return &p.Versions[i]
		}
	}
	return nil
}

// Time is a time as Thunderstore sends it. One that does not parse is left
// zero rather than failing the whole list.
type Time struct{ time.Time }

// UnmarshalJSON reads a time, leaving it zero when it doesn't parse.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		t.Time, _ = time.Parse(time.RFC3339Nano, s)
	}
	return nil
}

// Client reads one Thunderstore community: its packages, their zips and maps.
type Client struct {
	Community string // as in thunderstore.io/c/<community>/
	Base      string // https://thunderstore.io; tests point it elsewhere
	HTTP      *http.Client
	// UserAgent names the manager to Thunderstore, so they can tell who to ask
	// about its requests.
	UserAgent string

	mu       sync.Mutex
	pkgs     map[string]*Package // by lowercased full name
	err      error
	at       time.Time
	seen     listingSeen   // what pkgs was fetched from
	fetching chan struct{} // closed when the fetch under way ends; nil for none
}

// listingTimeout bounds a whole fetch of the listing, every chunk included.
const listingTimeout = 5 * time.Minute

// listingSeen is how the listing looked when it was fetched: its
// Last-Modified, and the address of its index, which is named after the
// index's SHA-256 and so changes with it.
type listingSeen struct {
	modified, index string
}

// New makes a client for a community on thunderstore.io.
func New(community string) *Client {
	return &Client{Community: community, Base: "https://thunderstore.io", HTTP: &http.Client{Timeout: 30 * time.Second}, UserAgent: "ReSkateManager"}
}

// Packages returns the community's packages by lowercased full name, cached
// for ten minutes. force skips the cache, unless it is under 30 seconds old,
// so a refresh button cannot have the manager hammer Thunderstore. A listing
// that has not changed since it was fetched is not fetched again.
//
// The fetch runs on its own, once for everyone waiting: a caller who gives
// up doesn't stop it, nor leave their reason cached for the others.
func (c *Client) Packages(ctx context.Context, force bool) (map[string]*Package, error) {
	c.mu.Lock()
	for {
		age := time.Since(c.at)
		if (age < 30*time.Second || !force && age < 10*time.Minute) && (c.pkgs != nil || c.err != nil) {
			defer c.mu.Unlock()
			return c.pkgs, c.err
		}
		if c.fetching == nil {
			c.fetching = make(chan struct{})
			go c.refresh(c.fetching, c.seen)
		}
		done := c.fetching
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c.mu.Lock()
	}
}

// refresh fetches the listing for Packages, then closes done.
func (c *Client) refresh(done chan struct{}, seen listingSeen) {
	ctx, cancel := context.WithTimeout(context.Background(), listingTimeout)
	defer cancel()
	pkgs, now, err := c.fetch(ctx, seen)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetching = nil
	close(done)
	c.at = time.Now()
	switch {
	case err == nil && pkgs == nil: // unchanged since it was fetched
	case err != nil && c.pkgs != nil: // an older list beats none
	default:
		c.pkgs, c.err = pkgs, err
		if err == nil {
			c.seen = now
		}
	}
}

// Fetched is when the package list was last fetched.
func (c *Client) Fetched() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// Limits on the listing, so a broken one cannot exhaust memory.
const (
	maxListingChunks = 1000
	maxListingBytes  = 256 << 20 // unpacked, across every chunk
)

// fetch reads the community's listing index: a redirect to a gzipped JSON
// list of addresses on Thunderstore's CDN, each of a gzipped chunk of the
// listing. It replaces the single listing at /api/v1/package/, which
// Thunderstore deprecated. A listing unchanged since seen answers no packages
// and no error: a 304 to If-Modified-Since, which Thunderstore documents but
// its CDN does not always give, or a redirect to the same index.
func (c *Client) fetch(ctx context.Context, seen listingSeen) (pkgs map[string]*Package, now listingSeen, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/c/"+url.PathEscape(c.Community)+"/api/v1/package-listing-index/", nil)
	if err != nil {
		return nil, now, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if seen.modified != "" {
		req.Header.Set("If-Modified-Since", seen.modified)
	}
	// The redirect is followed by hand, to see where it leads and when the listing changed.
	client := &http.Client{Timeout: c.HTTP.Timeout, Transport: c.HTTP.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, now, fmt.Errorf("thunderstore: %w", err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && seen.modified != "":
		return nil, seen, nil
	case resp.StatusCode < 300 || resp.StatusCode >= 400 || resp.StatusCode == http.StatusNotModified:
		return nil, now, fmt.Errorf("thunderstore: HTTP %d for the package listing", resp.StatusCode)
	}
	index, err := resp.Location()
	if err != nil {
		return nil, now, fmt.Errorf("thunderstore: %w", err)
	}
	now = listingSeen{modified: resp.Header.Get("Last-Modified"), index: index.String()}
	if now.index == seen.index {
		return nil, now, nil
	}
	var chunks []string
	budget := int64(1 << 20)
	if err := c.getListing(ctx, now.index, &budget, &chunks); err != nil {
		return nil, now, err
	}
	if len(chunks) > maxListingChunks {
		return nil, now, errors.New("thunderstore: the package listing has too many chunks")
	}
	pkgs = map[string]*Package{}
	budget = maxListingBytes
	for _, u := range chunks {
		var list []*Package
		if err := c.getListing(ctx, u, &budget, &list); err != nil {
			return nil, now, err
		}
		for _, p := range list {
			if p == nil || p.FullName == "" {
				continue // a null or nameless entry, from a listing gone wrong
			}
			pkgs[strings.ToLower(p.FullName)] = p
		}
	}
	return pkgs, now, nil
}

// getListing decodes the gzipped JSON at u, a part of the listing on
// Thunderstore, into v, taking what it unpacks to from budget.
func (c *Client) getListing(ctx context.Context, u string, budget *int64, v any) error {
	if pu, err := url.Parse(u); err != nil || !c.ours(pu) {
		return errors.New("thunderstore: the package listing points to another site")
	}
	resp, err := c.get(ctx, c.HTTP, u, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("thunderstore: package listing: %w", err)
	}
	defer gz.Close()
	r := &io.LimitedReader{R: gz, N: *budget + 1}
	err = json.NewDecoder(r).Decode(v)
	*budget = r.N - 1
	if *budget < 0 {
		return errors.New("thunderstore: the package listing is too big")
	}
	if err != nil {
		return fmt.Errorf("thunderstore: package listing: %w", err)
	}
	return nil
}

// ours reports whether u is on Thunderstore: its own host, or one under it
// such as its CDN.
func (c *Client) ours(u *url.URL) bool {
	base, err := url.Parse(c.Base)
	return err == nil && u.Scheme == base.Scheme && (u.Host == base.Host || strings.HasSuffix(u.Host, "."+base.Host))
}

// Download saves a version's zip to path, refusing one over limit bytes.
// progress, if set, hears how much has arrived and the size Thunderstore gave.
func (c *Client) Download(ctx context.Context, v *Version, path string, limit int64, progress func(done, total int64)) error {
	u, err := url.Parse(v.DownloadURL)
	if err != nil || u.Scheme+"://"+u.Host != c.Base {
		return errors.New("thunderstore gave a download link to another site")
	}
	if v.FileSize > limit {
		return fmt.Errorf("the mod is over %s", sizeText(limit))
	}
	// A big mod outlasts the list's timeout; ctx bounds the download instead.
	resp, err := c.get(ctx, &http.Client{Transport: c.HTTP.Transport}, v.DownloadURL, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := &counter{w: f, fn: func(n int64) {
		if progress != nil {
			progress(n, v.FileSize)
		}
	}}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("the mod is over %s", sizeText(limit))
	}
	return err
}

type counter struct {
	w  io.Writer
	n  int64
	fn func(int64)
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.fn(c.n)
	return n, err
}

// ErrCannotLook is a zip that cannot be read without downloading it. Its
// address is worked out from its icon's, which fails for long names:
// Thunderstore shortens those and adds a random suffix.
var ErrCannotLook = errors.New("this mod's zip is kept where the manager cannot look inside it; its maps show once it is installed")

var errNotFound = errors.New("HTTP 404")

// Zip is a zip read where Thunderstore keeps it.
type Zip struct {
	*zip.Reader
	rr *rangeReader
}

// Err is the last failure to fetch part of the zip, which reading a file
// from it reports only as a broken file.
func (z *Zip) Err() error { return z.rr.err }

// OpenZip reads version v's zip where Thunderstore keeps it, fetching only
// the parts the zip reader asks for: its directory, then whichever files
// are opened. A mod's maps can be listed this way without downloading it.
func (c *Client) OpenZip(ctx context.Context, v *Version) (*Zip, error) {
	u, err := c.zipURL(v)
	if err != nil {
		return nil, err
	}
	rr := &rangeReader{ctx: ctx, c: c, url: u}
	if err := rr.fetchTail(); errors.Is(err, errNotFound) {
		return nil, ErrCannotLook
	} else if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(rr, rr.size)
	if errors.Is(err, zip.ErrInsecurePath) {
		err = nil // usable; the caller checks each path
	}
	if err != nil {
		if rr.err != nil {
			return nil, rr.err
		}
		return nil, errors.New("not a zip file")
	}
	return &Zip{zr, rr}, nil
}

// zipURL is where v's zip is stored. The download link counts a download and
// redirects there, but looking inside a mod should not count as one, so the
// address is worked out from the icon's, which sits beside the zip:
// .../repository/icons/<name>.png and .../repository/packages/<name>.zip.
func (c *Client) zipURL(v *Version) (string, error) {
	u, err := url.Parse(v.Icon)
	if err != nil || !c.ours(u) {
		return "", ErrCannotLook
	}
	dir, file := path.Split(u.Path)
	name, ok := strings.CutSuffix(file, ".png")
	if !ok || path.Base(dir) != "icons" {
		return "", ErrCannotLook
	}
	u.Path = path.Join(path.Dir(path.Clean(dir)), "packages", name+".zip")
	u.RawQuery = ""
	return u.String(), nil
}

// Limits on what reading inside one zip may fetch.
const (
	rangeBlock    = 256 << 10
	rangeMaxBytes = 16 << 20
	rangeMaxReads = 40
)

// rangeReader reads a file over HTTP in blocks, keeping each it fetches. The
// first is the file's tail, where a zip keeps its directory.
type rangeReader struct {
	ctx  context.Context
	c    *Client
	url  string
	size int64

	blocks  []rangeBlockData
	fetched int64
	reads   int
	err     error // the last fetch's, as zip hides it
}

type rangeBlockData struct {
	off  int64
	data []byte
}

// tailBytes is how much of a zip's end is fetched first, for its directory.
const tailBytes = 128 << 10

func (r *rangeReader) fetchTail() error {
	resp, err := r.c.get(r.ctx, r.c.HTTP, r.url, fmt.Sprintf("bytes=-%d", tailBytes))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	start, end, size, err := contentRange(resp)
	if err != nil {
		return err
	}
	// The end of the zip, and no more than was asked for: these numbers are
	// the answer's, and the part is held in memory.
	if end != size-1 || end-start+1 > tailBytes {
		return errors.New("thunderstore sent the wrong part of the zip")
	}
	return r.keep(resp.Body, start, end-start+1, size)
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	n := 0
	for n < len(p) {
		at := off + int64(n)
		if at >= r.size {
			return n, io.EOF
		}
		b := r.block(at)
		if b == nil {
			if err := r.fetch(at, max(int64(len(p)-n), rangeBlock)); err != nil {
				r.err = err
				return n, err
			}
			continue
		}
		n += copy(p[n:], b.data[at-b.off:])
	}
	return n, nil
}

func (r *rangeReader) block(at int64) *rangeBlockData {
	for i := range r.blocks {
		if b := &r.blocks[i]; at >= b.off && at < b.off+int64(len(b.data)) {
			return b
		}
	}
	return nil
}

func (r *rangeReader) fetch(off, n int64) error {
	n = min(n, r.size-off)
	if r.reads >= rangeMaxReads || r.fetched+n > rangeMaxBytes {
		return errors.New("the zip is too big to look inside")
	}
	resp, err := r.c.get(r.ctx, r.c.HTTP, r.url, fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	start, end, _, err := contentRange(resp)
	if err != nil {
		return err
	}
	if start != off || end != off+n-1 {
		return errors.New("thunderstore sent the wrong part of the zip")
	}
	return r.keep(resp.Body, off, n, r.size)
}

func (r *rangeReader) keep(body io.Reader, off, n, size int64) error {
	data := make([]byte, n)
	if _, err := io.ReadFull(body, data); err != nil {
		return fmt.Errorf("thunderstore: %w", err)
	}
	r.size = size
	r.reads++
	r.fetched += n
	r.blocks = append(r.blocks, rangeBlockData{off, data})
	return nil
}

// contentRange reads where a 206 answer's part starts and ends, and the whole
// file's size.
func contentRange(resp *http.Response) (start, end, size int64, err error) {
	if resp.StatusCode != http.StatusPartialContent {
		return 0, 0, 0, errors.New("thunderstore cannot send part of a zip")
	}
	if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &size); err != nil || start < 0 || end < start || size <= end {
		return 0, 0, 0, errors.New("thunderstore sent a bad Content-Range")
	}
	return start, end, size, nil
}

// get fetches addr; with rng, only that byte range of it.
func (c *Client) get(ctx context.Context, client *http.Client, addr, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	// Redirects stay on Thunderstore too, such as from a download link to its
	// CDN: an answer that could send the manager anywhere could have it fetch
	// from the machine's own network.
	cl := *client
	cl.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !c.ours(next.URL) {
			return fmt.Errorf("redirected off Thunderstore, to %s", next.URL.Host)
		}
		return nil
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("thunderstore: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("thunderstore: %w", errNotFound)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("thunderstore: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// sizeText writes a size limit for people, in GB or, under one, MB.
func sizeText(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%d GB", n>>30)
	}
	return fmt.Sprintf("%d MB", n>>20)
}
