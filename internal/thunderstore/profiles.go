package thunderstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/xThrasherrr/ReSkateManager/internal/modprofile"
)

// Mod profiles travel as codes on Thunderstore's profile service, which
// ReSkate's launcher (2.0.8) and r2modman share: a payload sent to create
// is kept for a while under the code it answers.
const (
	profilePath    = "/api/experimental/legacyprofile/"
	profileTimeout = 30 * time.Second
	maxCreated     = 64 << 10
)

// ShareProfile puts a profile's payload on the profile service and returns
// its code. Thunderstore keeps codes for a limited time.
func (c *Client) ShareProfile(ctx context.Context, payload []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+profilePath+"create/", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	cl := *c.HTTP
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("thunderstore: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("thunderstore: profile service: HTTP %d", resp.StatusCode)
	}
	var answer struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCreated)).Decode(&answer); err != nil || !modprofile.ValidCode(answer.Key) {
		return "", errors.New("thunderstore answered without a profile code")
	}
	return answer.Key, nil
}

// FetchProfile is the payload a profile code names.
func (c *Client) FetchProfile(ctx context.Context, code string) ([]byte, error) {
	if !modprofile.ValidCode(code) {
		return nil, errors.New("a profile code is 8 to 64 letters a to f, digits and dashes")
	}
	ctx, cancel := context.WithTimeout(ctx, profileTimeout)
	defer cancel()
	resp, err := c.get(ctx, c.HTTP, c.Base+profilePath+"get/"+url.PathEscape(code)+"/", "")
	if errors.Is(err, errNotFound) {
		return nil, errors.New("thunderstore has no profile with that code; codes last a limited time")
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, modprofile.MaxPayload+1))
	if err != nil {
		return nil, fmt.Errorf("thunderstore: %w", err)
	}
	return b, nil
}
