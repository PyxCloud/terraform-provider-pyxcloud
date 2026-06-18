package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenSource yields a bearer for the PyxCloud API. There are two ways the
// provider authenticates, and NEITHER logs in a human pyxcloud/passobuild user:
//
//   - Machine (preferred): OAuth 2.1 client_credentials against the passobuild
//     realm token endpoint, using the provider's OWN confidential client
//     (client_id + client_secret). This authenticates the *provider execution*,
//     not a user; only a caller holding the provider client's credentials can mint
//     a token, so the /api/* surface is not usable by arbitrary third parties.
//   - Static: a pre-issued bearer (PYXCLOUD_TOKEN), for tests / break-glass.
//
// The machine source caches the token and refreshes it shortly before expiry.
type tokenSource interface {
	token(ctx context.Context) (string, error)
}

// staticToken is a fixed pre-issued bearer.
type staticToken string

func (s staticToken) token(_ context.Context) (string, error) { return string(s), nil }

// clientCredentialsSource fetches + caches a token via OAuth2.1 client_credentials.
type clientCredentialsSource struct {
	tokenURL     string
	clientID     string
	clientSecret string
	httpc        *http.Client

	mu      sync.Mutex
	cached  string
	expires time.Time
}

func newClientCredentialsSource(tokenURL, clientID, clientSecret string) *clientCredentialsSource {
	return &clientCredentialsSource{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		httpc:        &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *clientCredentialsSource) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Reuse while still valid (with a 30s safety margin).
	if c.cached != "" && time.Until(c.expires) > 30*time.Second {
		return c.cached, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("client_credentials token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("client_credentials grant failed (%d): %s", resp.StatusCode, msg)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("token response had no access_token")
	}
	ttl := tr.ExpiresIn
	if ttl <= 0 {
		ttl = 300
	}
	c.cached = tr.AccessToken
	c.expires = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.cached, nil
}
