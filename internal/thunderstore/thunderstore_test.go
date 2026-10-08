package thunderstore

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// listing serves pkgs, a JSON array of packages, as Thunderstore's listing
// index does: a redirect to a gzipped list of chunk addresses, each a gzipped
// part of the listing. A request that has seen the listing as last modified
// gets 304. It counts the requests for the index and for chunks.
type listing struct {
	pkgs          string       // {base} stands for the server's address
	modified      atomic.Int64 // unix seconds; the index's address changes with it
	ignoreSince   atomic.Bool  // never answer 304, as Thunderstore's CDN does
	elsewhere     atomic.Bool  // the index points at a chunk on another site
	index, chunks atomic.Int32
}

func (l *listing) serve(w http.ResponseWriter, r *http.Request, base string) bool {
	switch {
	case r.URL.Path == "/c/reskate/api/v1/package-listing-index/":
		l.index.Add(1)
		modified := time.Unix(l.modified.Load(), 0)
		if t, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !modified.After(t) && !l.ignoreSince.Load() {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
		w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
		http.Redirect(w, r, fmt.Sprintf("%s/blob/index-%d", base, modified.Unix()), http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/blob/index-"):
		chunk := base + "/blob/chunk"
		if l.elsewhere.Load() {
			chunk = "https://example.com/chunk"
		}
		writeGzip(w, `["`+chunk+`"]`)
	case r.URL.Path == "/blob/chunk":
		l.chunks.Add(1)
		writeGzip(w, strings.ReplaceAll(l.pkgs, "{base}", base))
	default:
		return false
	}
	return true
}

func writeGzip(w io.Writer, s string) {
	zw := gzip.NewWriter(w)
	zw.Write([]byte(s))
	zw.Close()
}

func TestPackagesAndDownload(t *testing.T) {
	l := &listing{pkgs: `[{"full_name":"Sk8r-BBCity","package_url":"{base}/c/reskate/p/Sk8r/BBCity/","date_updated":"2026-10-05T21:00:27.221661Z","date_created":"","versions":[
		{"version_number":"1.1.0","download_url":"{base}/package/download/Sk8r/BBCity/1.1.0/","file_size":3},
		{"version_number":"1.0.0","download_url":"{base}/package/download/Sk8r/BBCity/1.0.0/","file_size":3}]}]`}
	l.modified.Store(time.Now().Add(-time.Hour).Unix())
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.serve(w, r, srv.URL) {
			return
		}
		switch r.URL.Path {
		case "/package/download/Sk8r/BBCity/1.1.0/":
			if !strings.HasPrefix(r.UserAgent(), "ReSkateManager/1.2.3 ") {
				http.Error(w, "who are you", http.StatusForbidden)
				return
			}
			w.Write([]byte("zip"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("reskate")
	c.Base = srv.URL
	c.UserAgent = "ReSkateManager/1.2.3 (+https://example.com)"

	ctx := context.Background()
	pkgs, err := c.Packages(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	p := pkgs["sk8r-bbcity"]
	if p == nil || p.Latest().Number != "1.1.0" || p.Version("1.0.0") == nil || p.Version("9") != nil {
		t.Fatalf("%+v", pkgs)
	}
	// A date that does not parse is left zero rather than failing the list.
	if p.Updated.Year() != 2026 || !p.Created.IsZero() {
		t.Errorf("dates: updated %v, created %v", p.Updated, p.Created)
	}
	if c.Packages(ctx, false); l.index.Load() != 1 {
		t.Errorf("asked for the listing %d times, want it cached", l.index.Load())
	}
	// A forced refresh straight after a fetch is held back too.
	if c.Packages(ctx, true); l.index.Load() != 1 {
		t.Errorf("asked for the listing %d times, want a forced refresh held back", l.index.Load())
	}
	// A later one asks again, but an unchanged listing is not fetched again:
	// the answer is a 304, or, where If-Modified-Since is ignored, a redirect
	// to the same index.
	for i, ignore := range []bool{false, true} {
		l.ignoreSince.Store(ignore)
		c.at = c.at.Add(-time.Minute)
		if again, err := c.Packages(ctx, true); err != nil || again["sk8r-bbcity"] == nil || l.index.Load() != int32(2+i) || l.chunks.Load() != 1 {
			t.Errorf("unchanged listing (If-Modified-Since ignored: %v): %v, %d asks, %d chunks fetched; want %d asks, 1 chunk", ignore, err, l.index.Load(), l.chunks.Load(), 2+i)
		}
	}
	l.modified.Add(60)
	c.at = c.at.Add(-time.Minute)
	if c.Packages(ctx, true); l.chunks.Load() != 2 {
		t.Errorf("changed listing: %d chunks fetched, want 2", l.chunks.Load())
	}

	// The listing's chunks must be on Thunderstore.
	l.elsewhere.Store(true)
	other := New("reskate")
	other.Base = srv.URL
	if _, err := other.Packages(ctx, false); err == nil || !strings.Contains(err.Error(), "another site") {
		t.Errorf("a chunk on another site: %v", err)
	}

	to := filepath.Join(t.TempDir(), "mod.zip")
	var done, total int64
	if err := c.Download(ctx, p.Latest(), to, 1<<20, func(d, t int64) { done, total = d, t }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(to); string(b) != "zip" {
		t.Errorf("downloaded %q", b)
	}
	if done != 3 || total != 3 {
		t.Errorf("progress %d of %d, want 3 of 3", done, total)
	}
	if err := c.Download(ctx, p.Latest(), to, 2, nil); err == nil {
		t.Error("downloaded a mod over the limit")
	}
	elsewhere := &Version{Number: "9", DownloadURL: "https://example.com/x.zip"}
	if err := c.Download(ctx, elsewhere, to, 1<<20, nil); err == nil || !strings.Contains(err.Error(), "another site") {
		t.Errorf("download from another site: %v", err)
	}
}

// OpenZip reads a zip's directory and the files opened from it with range
// requests, from beside the icon rather than through the download link.
func TestOpenZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("reskate-levels.json")
	w.Write([]byte(`{"levels":[]}`))
	// Enough incompressible bytes that the start of the zip is not in its tail.
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "Win32/big.cas", Method: zip.Store})
	big := make([]byte, 600<<10)
	for i := range big {
		big[i] = byte(i * 7919 >> 3)
	}
	w.Write(big)
	zw.Close()
	zipped := buf.Bytes()

	var reads, downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live/repository/packages/Sk8r-BBCity-1.0.0.zip":
			reads.Add(1)
			http.ServeContent(w, r, "x.zip", time.Time{}, bytes.NewReader(zipped))
		case "/package/download/Sk8r/BBCity/1.0.0/":
			downloads.Add(1)
			w.Write(zipped)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New("reskate")
	c.Base = srv.URL
	v := &Version{FullName: "Sk8r-BBCity-1.0.0", Icon: srv.URL + "/live/repository/icons/Sk8r-BBCity-1.0.0.png", DownloadURL: srv.URL + "/package/download/Sk8r/BBCity/1.0.0/"}

	z, err := c.OpenZip(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 2 || z.File[0].Name != "reskate-levels.json" {
		t.Fatalf("files: %v", z.File)
	}
	rc, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(b) != `{"levels":[]}` || z.Err() != nil {
		t.Fatalf("read %q: %v, %v", b, err, z.Err())
	}
	if n := reads.Load(); n < 2 || n > 3 {
		t.Errorf("%d range reads, want the tail and one block", n)
	}
	if downloads.Load() != 0 {
		t.Error("looking inside went through the download link, which counts a download")
	}

	// Thunderstore stores a long name's zip under a shortened name of its
	// own, so it is not beside the icon.
	for _, icon := range []string{"https://example.com/live/repository/icons/Sk8r-BBCity-1.0.0.png", srv.URL + "/somewhere/else.png", "",
		srv.URL + "/live/repository/icons/Nobody-Nothing-1.0.0.png"} {
		if _, err := c.OpenZip(context.Background(), &Version{Icon: icon}); !errors.Is(err, ErrCannotLook) {
			t.Errorf("opened a zip beside icon %q: %v", icon, err)
		}
	}
}

// A Content-Range is the answer's word, and what is kept in memory is sized
// from it: one claiming far more than was asked for is refused, not allocated.
func TestOpenZipBadContentRange(t *testing.T) {
	for name, header := range map[string]string{
		"a whole huge file": "bytes 0-0/4611686018427387904",
		"more than asked":   "bytes 0-999999/1000000",
		"not the end":       "bytes 0-9/1000000",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", header)
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("PK"))
		}))
		c := New("reskate")
		c.Base = srv.URL
		v := &Version{FullName: "Sk8r-BBCity-1.0.0", Icon: srv.URL + "/live/repository/icons/Sk8r-BBCity-1.0.0.png"}
		if _, err := c.OpenZip(context.Background(), v); err == nil {
			t.Errorf("%s: opened", name)
		}
		srv.Close()
	}
}

// A listing fetch belongs to no one caller: one who gives up doesn't stop it
// for the rest, nor leave "context canceled" cached for ten minutes.
func TestPackagesOutlivesItsFirstCaller(t *testing.T) {
	release := make(chan struct{})
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/c/reskate/api/v1/package-listing-index/":
			<-release
			http.Redirect(w, r, srv.URL+"/index.json.gz", http.StatusFound)
		case "/index.json.gz":
			gzipJSON(w, []string{srv.URL + "/chunk.json.gz"})
		case "/chunk.json.gz":
			gzipJSON(w, []any{nil, map[string]any{"full_name": "Sk8r-BBCity", "versions": []any{}}})
		}
	}))
	t.Cleanup(srv.Close)
	c := New("reskate")
	c.Base = srv.URL

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := c.Packages(ctx, false)
		first <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the first caller: %v", err)
	}
	close(release)
	pkgs, err := c.Packages(context.Background(), false)
	if err != nil || pkgs["sk8r-bbcity"] == nil || len(pkgs) != 1 {
		t.Fatalf("the next caller: %v, %v", pkgs, err)
	}
}

func gzipJSON(w http.ResponseWriter, v any) {
	gz := gzip.NewWriter(w)
	json.NewEncoder(gz).Encode(v)
	gz.Close()
}

// A download link may lead to Thunderstore's CDN, and nowhere else.
func TestDownloadStaysOnThunderstore(t *testing.T) {
	var reached atomic.Bool
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	t.Cleanup(inside.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inside.URL+"/admin", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := New("reskate")
	c.Base = srv.URL
	v := &Version{FullName: "Sk8r-BBCity-1.0.0", DownloadURL: srv.URL + "/package/download/Sk8r/BBCity/1.0.0/", FileSize: 10}
	err := c.Download(context.Background(), v, filepath.Join(t.TempDir(), "x.zip"), 1<<20, nil)
	if err == nil || reached.Load() {
		t.Fatalf("followed a redirect off Thunderstore: %v", err)
	}
}
