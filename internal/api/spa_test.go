package api

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Hashed bundles are cached for good; files that keep their name across
// releases are checked again each time.
func TestStaticCaching(t *testing.T) {
	a := &API{Static: fstest.MapFS{
		"index.html":                {Data: []byte("<html>")},
		"favicon.png":               {Data: []byte("png")},
		"_app/immutable/app.abc.js": {Data: []byte("js")},
	}}
	for path, want := range map[string]string{
		"/favicon.png":               "no-cache",
		"/_app/immutable/app.abc.js": "public, max-age=31536000, immutable",
		"/s/a/mods":                  "no-cache",
	} {
		w := httptest.NewRecorder()
		a.spa().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", path, got, want)
		}
	}
}

// Nothing the API answers is cached, even refused or unknown requests.
func TestAPINotCached(t *testing.T) {
	h := (&API{Static: fstest.MapFS{}}).Handler()
	for _, path := range []string{"/api/health", "/api/backups/backup-20261006-040000-scheduled.zip", "/api/nope"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s (%d): Cache-Control %q, want no-store", path, w.Code, got)
		}
	}
}

// The page's inline scripts are allowed by hash, so no other inline script runs.
func TestCSPHashesInlineScripts(t *testing.T) {
	a := &API{Static: fstest.MapFS{
		"index.html": {Data: []byte("<html><script src=\"/app.js\"></script><div>\r\n<script>\r\n\tstart();\r\n</script></div>")},
	}}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	sum := sha256.Sum256([]byte("\n\tstart();\n")) // as a browser hashes it, line breaks made \n
	want := "script-src 'self' 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "';"
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want) {
		t.Errorf("CSP %q\nwant it to hold %q", csp, want)
	}
}
