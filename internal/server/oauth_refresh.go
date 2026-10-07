package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	oauthDefaultTokenField = "access_token"
	oauthDefaultLifetime   = 5 * time.Minute
	oauthExpiryLeeway      = 60 * time.Second
	oauthMaxResponseBytes  = 1 << 20
)

// oauthTokenCache exchanges a stored refresh token for a short-lived bearer
// and holds the result until shortly before it expires. Entries are keyed by a
// digest of (token URL, client id, refresh token), so a refresh token rotated
// in OpenBao is picked up on the next request rather than after expiry, and no
// credential value is held as a map key.
type oauthTokenCache struct {
	mu      sync.Mutex
	entries map[string]oauthToken
	client  *http.Client
	now     func() time.Time
}

type oauthToken struct {
	value   string
	expires time.Time
}

func newOAuthTokenCache() *oauthTokenCache {
	return &oauthTokenCache{
		entries: make(map[string]oauthToken),
		client:  &http.Client{Timeout: 10 * time.Second},
		now:     time.Now,
	}
}

// bearer returns a live bearer for refreshToken. force skips the cache; the
// 401 refresh path sets it so a token the upstream just rejected is not reused.
// Errors never carry response bodies, which can echo credentials.
func (c *oauthTokenCache) bearer(ctx context.Context, injectAs *InjectAs, refreshToken string, force bool) (string, error) {
	sum := sha256.Sum256([]byte(injectAs.TokenURL + "\x00" + injectAs.ClientID + "\x00" + refreshToken))
	key := hex.EncodeToString(sum[:])

	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok && !force && c.now().Before(entry.expires) {
		return entry.value, nil
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", injectAs.ClientID)
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, injectAs.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oauth-refresh: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth-refresh: token request failed: %w", urlErrorWithoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth-refresh: token endpoint returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("oauth-refresh: reading token response: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("oauth-refresh: token response is not JSON")
	}
	field := injectAs.TokenField
	if field == "" {
		field = oauthDefaultTokenField
	}
	value, _ := payload[field].(string)
	if value == "" {
		return "", fmt.Errorf("oauth-refresh: token response has no %q field", field)
	}

	c.entries[key] = oauthToken{value: value, expires: c.expiry(value, payload)}
	return value, nil
}

// expiry prefers the token's own JWT exp claim, then expires_in, then a
// conservative default, and always leaves a safety leeway.
func (c *oauthTokenCache) expiry(token string, payload map[string]any) time.Time {
	now := c.now()
	lifetime := oauthDefaultLifetime
	if exp, ok := jwtExpiry(token); ok {
		lifetime = exp.Sub(now)
	} else if secs, ok := payload["expires_in"].(float64); ok && secs > 0 {
		lifetime = time.Duration(secs) * time.Second
	}
	return now.Add(lifetime - oauthExpiryLeeway)
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// urlErrorWithoutURL drops the request URL from a transport error.
func urlErrorWithoutURL(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}
