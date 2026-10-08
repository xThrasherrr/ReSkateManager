package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfChecker(t *testing.T) {
	tag := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/m/releases/latest" || tag == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/o/m/releases/tag/` + tag + `"}`))
	}))
	defer srv.Close()
	s := &SelfChecker{Repo: "o/m", Current: "1.2.3", API: srv.URL}

	for _, c := range []struct {
		tag, want string
	}{
		{"", ""}, // nothing published
		{"v1.2.3", ""},
		{"v1.2.2", ""},
		{"v1.10.0", "1.10.0"},
		{"v2.0.0-rc1", ""},
		{"v2.0.0-rc.1", ""}, // a release never takes a candidate, though GitHub's latest were one
	} {
		tag = c.tag
		if err := s.Check(context.Background()); err != nil {
			t.Fatalf("%q: %v", c.tag, err)
		}
		got := ""
		if rel := s.Newer(); rel != nil {
			got = rel.Version
		}
		if got != c.want {
			t.Errorf("latest %q: newer %q, want %q", c.tag, got, c.want)
		}
	}

	// A dev build never reports an update.
	tag = "v9.0.0"
	dev := &SelfChecker{Repo: "o/m", Current: "dev", API: srv.URL}
	if err := dev.Check(context.Background()); err != nil || dev.Newer() != nil {
		t.Errorf("dev build: %v, %+v", err, dev.Newer())
	}
}

// A release candidate looks through the list, newest first as GitHub gives
// it, and takes the newest candidate or release after its own.
func TestSelfCheckerCandidate(t *testing.T) {
	var tags []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/m/releases" {
			http.NotFound(w, r)
			return
		}
		var out []string
		for _, tag := range tags {
			draft := strings.HasSuffix(tag, "+draft")
			tag = strings.TrimSuffix(tag, "+draft")
			out = append(out, `{"tag_name":"`+tag+`","html_url":"https://github.com/o/m/releases/tag/`+tag+`","draft":`+map[bool]string{true: "true", false: "false"}[draft]+`}`)
		}
		w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	}))
	defer srv.Close()
	s := &SelfChecker{Repo: "o/m", Current: "1.0.0-rc.2", API: srv.URL}

	for _, c := range []struct {
		tags []string
		want string
	}{
		{nil, ""},
		{[]string{"v1.0.0-rc.2", "v1.0.0-rc.1", "v0.9.7"}, ""},
		{[]string{"v1.0.0-rc.10", "v1.0.0-rc.2"}, "1.0.0-rc.10"}, // numbers, not text
		{[]string{"v1.0.0", "v1.0.0-rc.3", "v1.0.0-rc.2"}, "1.0.0"},
		{[]string{"v1.0.0-rc.3", "v1.0.0"}, "1.0.0"}, // whatever order they come in
		{[]string{"v1.1.0-rc.1", "v1.0.1", "v1.0.0"}, "1.1.0-rc.1"},
		{[]string{"v1.0.0+draft", "v1.0.0-rc.1"}, ""},
		{[]string{"nightly", "v1.0.0-beta.1", "v1.0.0-rc3"}, ""},
	} {
		tags = c.tags
		if err := s.Check(context.Background()); err != nil {
			t.Fatalf("%q: %v", c.tags, err)
		}
		got := ""
		if rel := s.Newer(); rel != nil {
			got = rel.Version
		}
		if got != c.want {
			t.Errorf("releases %q: newer %q, want %q", c.tags, got, c.want)
		}
	}
}

func TestVersionOrder(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"0.9.7", "1.0.0-rc.1", true},
		{"1.0.0-rc.1", "1.0.0-rc.2", true},
		{"1.0.0-rc.9", "1.0.0-rc.10", true},
		{"1.0.0-rc.10", "1.0.0", true},
		{"1.0.0", "1.0.0-rc.10", false},
		{"v1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1-rc.1", true},
	} {
		a, b := parseVersion(c.a), parseVersion(c.b)
		if a == nil || b == nil {
			t.Fatalf("%q or %q unread", c.a, c.b)
		}
		if versionLess(a, b) != c.less {
			t.Errorf("%s < %s: %v, want %v", c.a, c.b, !c.less, c.less)
		}
	}
}
