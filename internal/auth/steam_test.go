package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSteamID = "76561198000000001"

// fakeSteam stands in for Steam's OpenID provider. check_authentication
// confirms an assertion only when its signature is "good" and every signed
// field came back unchanged; checks counts the times it was asked.
func fakeSteam(t *testing.T) (s *Steam, checks *atomic.Int32) {
	t.Helper()
	checks = new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks.Add(1)
		if r.Method != http.MethodPost {
			http.Error(w, "only check_authentication is faked", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		ok := r.PostForm.Get("openid.mode") == "check_authentication" && r.PostForm.Get("openid.sig") == "good"
		for f := range strings.SplitSeq(r.PostForm.Get("openid.signed"), ",") {
			if r.PostForm.Get("openid."+f) == "" {
				ok = false
			}
		}
		w.Write([]byte("ns:http://specs.openid.net/auth/2.0\nis_valid:" + map[bool]string{true: "true", false: "false"}[ok] + "\n"))
	}))
	t.Cleanup(srv.Close)
	s = NewSteam()
	s.Endpoint = srv.URL
	return s, checks
}

// freshNonce is a response_nonce as Steam makes them: the time, then something unique.
func freshNonce() string {
	return time.Now().UTC().Format(time.RFC3339) + rand.Text()
}

// steamAssertion builds the query Steam appends to return_to after a sign-in.
func steamAssertion(s *Steam, returnTo, steamID, sig string) url.Values {
	id := "https://steamcommunity.com/openid/id/" + steamID
	return url.Values{
		"openid.ns":             {"http://specs.openid.net/auth/2.0"},
		"openid.mode":           {"id_res"},
		"openid.op_endpoint":    {s.Endpoint},
		"openid.claimed_id":     {id},
		"openid.identity":       {id},
		"openid.return_to":      {returnTo},
		"openid.response_nonce": {freshNonce()},
		"openid.assoc_handle":   {"1234567890"},
		"openid.signed":         {"signed,op_endpoint,claimed_id,identity,return_to,response_nonce,assoc_handle"},
		"openid.sig":            {sig},
	}
}

func TestSteamRedirect(t *testing.T) {
	u, err := url.Parse(NewSteam().Redirect("http://127.0.0.1:40120/api/auth/steam/callback?n=abc", "http://127.0.0.1:40120"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if got := u.Scheme + "://" + u.Host + u.Path; got != SteamLogin {
		t.Fatalf("endpoint = %s", got)
	}
	if q.Get("openid.mode") != "checkid_setup" || q.Get("openid.return_to") != "http://127.0.0.1:40120/api/auth/steam/callback?n=abc" || q.Get("openid.realm") != "http://127.0.0.1:40120" {
		t.Fatalf("query = %v", q)
	}
}

func TestSteamVerify(t *testing.T) {
	s, checks := fakeSteam(t)
	const returnTo = "http://panel.test/api/auth/steam/callback?n=abc"
	ctx := context.Background()

	got, err := s.Verify(ctx, steamAssertion(s, returnTo, testSteamID, "good"), returnTo)
	if err != nil || got != testSteamID {
		t.Fatalf("valid assertion: got %q, %v", got, err)
	}

	const victim = "76561198000000002"
	cases := map[string]struct {
		mutate func(q url.Values)
		want   error
	}{
		"cancelled":         {func(q url.Values) { q.Set("openid.mode", "cancel") }, ErrSteamCancelled},
		"bad signature":     {func(q url.Values) { q.Set("openid.sig", "forged") }, ErrSteamRejected},
		"other provider":    {func(q url.Values) { q.Set("openid.op_endpoint", "https://evil.test/openid/login") }, ErrSteamRejected},
		"other return_to":   {func(q url.Values) { q.Set("openid.return_to", "http://evil.test/cb") }, ErrSteamRejected},
		"identity mismatch": {func(q url.Values) { q.Set("openid.identity", "https://steamcommunity.com/openid/id/"+victim) }, ErrSteamRejected},
		"not a SteamID64": {func(q url.Values) {
			id := "https://steamcommunity.com/openid/id/123"
			q.Set("openid.claimed_id", id)
			q.Set("openid.identity", id)
		}, ErrSteamRejected},
		"foreign claimed_id": {func(q url.Values) {
			id := "https://evil.test/openid/id/" + testSteamID
			q.Set("openid.claimed_id", id)
			q.Set("openid.identity", id)
		}, ErrSteamRejected},
		"claimed_id unsigned": {func(q url.Values) {
			q.Set("openid.signed", "signed,op_endpoint,identity,return_to,response_nonce,assoc_handle")
		}, ErrSteamRejected},
		// The genuine pair stays, signed; another rides in front of it. A
		// provider reading the last of each would confirm the genuine one.
		"second identity in front": {func(q url.Values) {
			id := "https://steamcommunity.com/openid/id/" + victim
			q["openid.claimed_id"] = append([]string{id}, q["openid.claimed_id"]...)
			q["openid.identity"] = append([]string{id}, q["openid.identity"]...)
		}, ErrSteamRejected},
		"repeated return_to": {func(q url.Values) { q.Add("openid.return_to", returnTo) }, ErrSteamRejected},
		"stale nonce": {func(q url.Values) {
			q.Set("openid.response_nonce", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339)+"x")
		}, ErrSteamStale},
		"malformed nonce": {func(q url.Values) { q.Set("openid.response_nonce", "abc") }, ErrSteamStale},
	}
	for name, c := range cases {
		q := steamAssertion(s, returnTo, testSteamID, "good")
		c.mutate(q)
		if id, err := s.Verify(ctx, q, returnTo); !errors.Is(err, c.want) {
			t.Errorf("%s: got %q, %v; want %v", name, id, err, c.want)
		}
	}

	q := steamAssertion(s, returnTo, testSteamID, "good")
	if _, err := s.Verify(ctx, q, returnTo); err != nil {
		t.Fatalf("first use: %v", err)
	}
	before := checks.Load()
	if id, err := s.Verify(ctx, q, returnTo); !errors.Is(err, ErrSteamStale) {
		t.Errorf("replayed assertion: got %q, %v", id, err)
	}
	if checks.Load() != before {
		t.Error("a replayed assertion was sent to Steam")
	}
}

func TestSteamUnreachable(t *testing.T) {
	s, _ := fakeSteam(t)
	q := steamAssertion(s, "http://panel.test/cb", testSteamID, "good")
	s.Endpoint = "http://127.0.0.1:1" // nothing listens there
	q.Set("openid.op_endpoint", s.Endpoint)
	if _, err := s.Verify(context.Background(), q, "http://panel.test/cb"); !errors.Is(err, ErrSteamUnreachable) {
		t.Fatalf("got %v", err)
	}
}

func TestNonceLog(t *testing.T) {
	var l nonceLog
	now := time.Now()
	n := now.UTC().Format(time.RFC3339) + "a"
	if !l.use(n, now) || l.use(n, now.Add(30*time.Minute)) {
		t.Fatal("a nonce must be accepted once")
	}
	// Remembered for as long as its time is within the window.
	if l.use(n, now.Add(nonceWindow)) {
		t.Fatal("nonce accepted again at the window's edge")
	}
	if l.use(n, now.Add(nonceWindow+time.Second)) {
		t.Fatal("expired nonce accepted")
	}
	if !l.use(now.Add(-50*time.Minute).UTC().Format(time.RFC3339)+"b", now) {
		t.Fatal("a nonce from a slow clock is refused")
	}
}

func TestIsValid(t *testing.T) {
	for body, want := range map[string]bool{
		"ns:http://specs.openid.net/auth/2.0\nis_valid:true\n":  true,
		"ns:http://specs.openid.net/auth/2.0\nis_valid:false\n": false,
		"is_valid:trueish\n":                   false,
		"note:is_valid:true\nis_valid:false\n": false,
		"":                                     false,
	} {
		if got := isValid(body); got != want {
			t.Errorf("isValid(%q) = %v", body, got)
		}
	}
}
