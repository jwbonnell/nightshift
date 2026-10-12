package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds every individual forge HTTP request, including
// each page of a paginated listing (P8.6).
const requestTimeout = 30 * time.Second

// NewHTTPClient builds an *http.Client whose CheckRedirect refuses to
// follow a redirect off apiHost, so a token is never sent to a different
// host. apiHost is compared case-insensitively, port included, against
// the redirect target — never against the first request's host, so a
// client always protects its own configured API host regardless of how
// it got there. urlSetting names the env/config key the error message
// should point the user at (NIGHTSHIFT_GITLAB_URL / NIGHTSHIFT_GITHUB_URL).
func NewHTTPClient(apiHost, urlSetting string) *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("forge: stopped after 10 redirects")
			}
			if !strings.EqualFold(req.URL.Host, apiHost) {
				return fmt.Errorf("forge: refusing redirect from %s to %s (different host; the token is never sent off-host) — check %s", apiHost, req.URL.Host, urlSetting)
			}
			return nil
		},
	}
}

// GetJSON issues a GET to rawURL with the given headers, under its own
// 30s timeout derived from ctx, and decodes the JSON response body into
// out. kind names the forge in error messages.
func GetJSON(ctx context.Context, c *http.Client, kind Kind, rawURL string, hdr map[string]string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d for %s (check URL/token/scope)", kind, resp.StatusCode, rawURL)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decode %s: %w", kind, rawURL, err)
	}
	return nil
}
