package thunderstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A payload sent to the profile service comes back under the code it
// answers, as the launcher and r2modman trade them.
func TestProfiles(t *testing.T) {
	kept := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/experimental/legacyprofile/create/":
			if r.Header.Get("Content-Type") != "application/octet-stream" || r.Header.Get("User-Agent") != "ReSkateManager" {
				http.Error(w, "bad request", 400)
				return
			}
			b, _ := io.ReadAll(r.Body)
			if strings.HasPrefix(string(b), "nokey") {
				w.Write([]byte(`{"error":"nope"}`))
				return
			}
			kept["018f2b1c-7d3e"] = b
			w.Write([]byte(`{"key":"018f2b1c-7d3e"}`))
		case r.URL.Path == "/api/experimental/legacyprofile/get/018f2b1c-7d3e/":
			w.Write(kept["018f2b1c-7d3e"])
		case r.URL.Path == "/api/experimental/legacyprofile/get/aaaaaaaa-0bad/":
			http.Redirect(w, r, "https://example.com/profile", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New("reskate")
	c.Base = srv.URL
	ctx := context.Background()

	code, err := c.ShareProfile(ctx, []byte("#r2modman\nUEsDBA=="))
	if err != nil || code != "018f2b1c-7d3e" {
		t.Fatalf("shared as %q, %v", code, err)
	}
	if b, err := c.FetchProfile(ctx, code); err != nil || string(b) != "#r2modman\nUEsDBA==" {
		t.Errorf("fetched %q, %v", b, err)
	}
	if _, err := c.ShareProfile(ctx, []byte("nokey")); err == nil {
		t.Error("an answer without a code was taken")
	}
	for code, want := range map[string]string{
		"0123abcd-0000": "no profile with that code",
		"../../etc":     "8 to 64",
		"aaaaaaaa-0bad": "redirected off Thunderstore",
	} {
		if _, err := c.FetchProfile(ctx, code); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("FetchProfile(%q) = %v, want %q", code, err, want)
		}
	}
}
