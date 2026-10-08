package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Steam sign-in is OpenID 2.0: send the browser to Steam, Steam sends it back
// with a signed assertion, and we ask Steam to confirm the signature.

// SteamLogin is Steam's OpenID provider.
const SteamLogin = "https://steamcommunity.com/openid/login"

// Why a Steam sign-in failed, for the panel to say. Any other reason Verify
// gives wraps ErrSteamRejected.
var (
	ErrSteamCancelled   = errors.New("sign-in with Steam was cancelled")
	ErrSteamUnreachable = errors.New("could not reach Steam to confirm the sign-in")
	ErrSteamStale       = errors.New("that sign-in with Steam was already used or is too old")
	ErrSteamRejected    = errors.New("the sign-in was not confirmed by Steam")
)

var claimedRe = regexp.MustCompile(`^https://steamcommunity\.com/openid/id/(7656119\d{10})$`)

// signedFields must all be covered by Steam's signature (OpenID 2.0 section
// 10.1); a field left out of it could be swapped without breaking it.
var signedFields = []string{"op_endpoint", "return_to", "response_nonce", "assoc_handle", "claimed_id", "identity"}

// Steam signs users in through an OpenID provider, Steam's own unless a test
// says otherwise.
type Steam struct {
	Endpoint string
	Client   *http.Client
	nonces   nonceLog
}

// NewSteam signs in through Steam itself.
func NewSteam() *Steam {
	return &Steam{Endpoint: SteamLogin, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Redirect is the URL that starts sign-in. returnTo must be an absolute URL on
// this panel; realm is its origin.
func (s *Steam) Redirect(returnTo, realm string) string {
	q := url.Values{
		"openid.ns":         {"http://specs.openid.net/auth/2.0"},
		"openid.mode":       {"checkid_setup"},
		"openid.return_to":  {returnTo},
		"openid.realm":      {realm},
		"openid.identity":   {"http://specs.openid.net/auth/2.0/identifier_select"},
		"openid.claimed_id": {"http://specs.openid.net/auth/2.0/identifier_select"},
	}
	return s.Endpoint + "?" + q.Encode()
}

// Verify checks the assertion Steam returned and gives the SteamID64.
// returnTo must equal the return_to that was sent, so an assertion made for
// another site cannot be replayed here.
func (s *Steam) Verify(ctx context.Context, query url.Values, returnTo string) (string, error) {
	// One value per field. These checks read the first of a repeated field
	// and Steam may read another, so a second claimed_id could otherwise ride
	// along on a genuine signature.
	for k, v := range query {
		if strings.HasPrefix(k, "openid.") && len(v) != 1 {
			return "", fmt.Errorf("%w: %s given %d times", ErrSteamRejected, k, len(v))
		}
	}
	switch query.Get("openid.mode") {
	case "id_res":
	case "cancel":
		return "", ErrSteamCancelled
	default:
		return "", fmt.Errorf("%w: mode %q", ErrSteamRejected, query.Get("openid.mode"))
	}
	if query.Get("openid.op_endpoint") != s.Endpoint {
		return "", fmt.Errorf("%w: provider %q", ErrSteamRejected, query.Get("openid.op_endpoint"))
	}
	if query.Get("openid.return_to") != returnTo {
		return "", fmt.Errorf("%w: returned to %q", ErrSteamRejected, query.Get("openid.return_to"))
	}
	m := claimedRe.FindStringSubmatch(query.Get("openid.claimed_id"))
	if m == nil || query.Get("openid.identity") != query.Get("openid.claimed_id") {
		return "", fmt.Errorf("%w: identity %q", ErrSteamRejected, query.Get("openid.claimed_id"))
	}
	signed := strings.Split(query.Get("openid.signed"), ",")
	for _, f := range signedFields {
		if !slices.Contains(signed, f) {
			return "", fmt.Errorf("%w: %s is not signed", ErrSteamRejected, f)
		}
	}
	// Before asking Steam, so a replayed assertion costs it no request.
	nonce := query.Get("openid.response_nonce")
	if !s.nonces.fresh(nonce, time.Now()) {
		return "", ErrSteamStale
	}
	if err := s.confirm(ctx, query); err != nil {
		return "", err
	}
	// Steam should confirm an assertion only once; don't count on it.
	if !s.nonces.use(nonce, time.Now()) {
		return "", ErrSteamStale
	}
	return m[1], nil
}

// confirm asks Steam whether it signed the assertion (check_authentication).
func (s *Steam) confirm(ctx context.Context, query url.Values) error {
	check := url.Values{}
	for k, v := range query {
		if strings.HasPrefix(k, "openid.") {
			check.Set(k, v[0])
		}
	}
	check.Set("openid.mode", "check_authentication")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, strings.NewReader(check.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSteamUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSteamUnreachable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: Steam answered %s", ErrSteamUnreachable, resp.Status)
	}
	if !isValid(string(body)) {
		return fmt.Errorf("%w: not valid", ErrSteamRejected)
	}
	return nil
}

// isValid reads the key-value form reply to check_authentication.
func isValid(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "is_valid" {
			return v == "true"
		}
	}
	return false
}

// nonceWindow is how far a response_nonce's time may be from ours. It is
// loose because a host's clock can drift; each nonce is remembered for twice
// as long, so none is forgotten while it would still be accepted.
const nonceWindow = time.Hour

// nonceLog remembers the response_nonce of each sign-in Steam confirmed.
type nonceLog struct {
	mu   sync.Mutex
	seen map[string]time.Time // nonce -> when it may be forgotten
}

// nonceTime reads the time a Steam nonce starts with, as 2006-01-02T15:04:05Z.
func nonceTime(nonce string) (time.Time, bool) {
	if len(nonce) < 20 {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, nonce[:20])
	return at, err == nil
}

// fresh reports whether a nonce is recent and unused, without using it.
func (l *nonceLog) fresh(nonce string, now time.Time) bool {
	at, ok := nonceTime(nonce)
	if !ok || at.Sub(now).Abs() > nonceWindow {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, used := l.seen[nonce]
	return !used
}

// use accepts a nonce the first time it is seen, while it is fresh.
func (l *nonceLog) use(nonce string, now time.Time) bool {
	at, ok := nonceTime(nonce)
	if !ok || at.Sub(now).Abs() > nonceWindow {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for n, until := range l.seen {
		if now.After(until) {
			delete(l.seen, n)
		}
	}
	if _, used := l.seen[nonce]; used {
		return false
	}
	if l.seen == nil {
		l.seen = map[string]time.Time{}
	}
	l.seen[nonce] = now.Add(2 * nonceWindow)
	return true
}
