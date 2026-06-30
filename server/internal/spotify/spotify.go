// Package spotify implements the Spotify Web API device-routing client and
// OAuth Authorization Code flow (design_doc §6, §9). It is a Go port of the
// legacy banovik/NameThatSpotify server.js Spotify logic (design_doc §10).
//
// Audio isolation (design_doc §9): the Go backend NEVER plays audio. It uses
// the Spotify Web API to route playback commands (PUT /v1/me/player/{play,
// pause}) to a specific device_id — the stage browser tab's authenticated
// "Virtual Device" registered via the Web Playback SDK. SetDevice binds that
// id; Play/Pause/Resume target it with the device_id query param. Latency-
// critical pause-on-buzz does NOT go through here — the engine messages the
// stage directly over WebTransport (§9).
//
// OAuth: AuthURL builds the authorize URL with the same three scopes the
// legacy core requested (server.js:1207). Exchange swaps the authorization
// code for access+refresh tokens. The client stores both and, mirroring the
// legacy refreshSpotifyToken/retry logic (server.js:1300-1311, 2283-2300),
// transparently refreshes the access token on a 401 and retries the call once.
//
// RUNTIME LIMITATION (honesty note): this package cannot be exercised
// end-to-end without a real Spotify Premium account, registered client
// credentials, a browser to complete the OAuth redirect, and an active Web
// Playback SDK device. None of that is available in CI. The package is
// therefore structured for offline unit testing: the HTTP client is injected
// (HTTPClient interface) so spotify_test.go drives every code path against an
// httptest.Server with zero real network calls. Treat green tests as proof of
// request shaping and refresh/retry control flow, NOT proof that playback
// works against live Spotify — that requires manual verification with creds.
package spotify

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

	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/game"
)

// Compile-time proof that *Client satisfies the engine's AudioDevice seam
// (game.ports.go). If the fixed contract changes, this fails to build.
var _ game.AudioDevice = (*Client)(nil)

// Spotify endpoints. Split into auth/account/API hosts so tests can leave them
// as-is (the injected HTTPClient intercepts the round-trip regardless of host).
const (
	authorizeURL = "https://accounts.spotify.com/authorize"
	tokenURL     = "https://accounts.spotify.com/api/token"
	apiBase      = "https://api.spotify.com/v1"
)

var scopes = []string{
	"streaming",
	"user-read-email",
	"user-read-private",
	"user-read-playback-state",
	"user-modify-playback-state",
	"user-read-currently-playing",
}

// HTTPClient is the minimal slice of *http.Client the client needs, kept as an
// interface so tests can inject an httptest-backed transport without the
// network.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client implements game.AudioDevice (design_doc §6). It is safe for
// concurrent use: token and device state are guarded by mu so the engine's
// command loop and the OAuth callback handler can touch it from different
// goroutines.
type Client struct {
	clientID     string
	clientSecret string
	redirectURI  string

	// HTTPClient is exported so callers/tests can swap the transport (e.g. an
	// httptest.Server-backed client). Defaults to http.DefaultClient via New.
	HTTPClient HTTPClient

	// endpoints are overridable in tests to point auth/token/api at httptest.
	authorizeURL string
	tokenURL     string
	apiBase      string

	// now is the clock used for token-expiry math, injectable for tests.
	now func() time.Time

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time // when the current access token dies (Spotify ~1h TTL)
	deviceID     string
}

// New builds a Client from config (design_doc §6, §9). Token/device fields
// start empty and are populated by Exchange (OAuth) and SetDevice.
func New(cfg *config.Config) *Client {
	return &Client{
		clientID:     cfg.SpotifyClientID,
		clientSecret: cfg.SpotifyClientSecret,
		redirectURI:  cfg.SpotifyRedirectURI,
		HTTPClient:   http.DefaultClient,
		authorizeURL: authorizeURL,
		tokenURL:     tokenURL,
		apiBase:      apiBase,
		now:          time.Now,
	}
}

// SetDevice binds the stage's Spotify device ID after OAuth (game.AudioDevice).
// All subsequent Play/Pause/Resume calls route to this device via the
// device_id query param.
func (c *Client) SetDevice(deviceID string) {
	c.mu.Lock()
	c.deviceID = deviceID
	c.mu.Unlock()
}

// AuthURL builds the Authorization Code authorize URL (game.AudioDevice),
// porting the legacy createAuthorizeURL call (server.js:1206-1209). state is
// the CSRF/round-trip token the caller verifies on the callback.
func (c *Client) AuthURL(state string) string {
	q := url.Values{}
	q.Set("client_id", c.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", c.redirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("show_dialog", "true")
	return c.authorizeURL + "?" + q.Encode()
}

// tokenResponse is the subset of the Spotify token payload we store. A refresh
// grant may omit refresh_token (the existing one stays valid), so it is
// pointer-free and simply left empty in that case.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds until the access token dies (~3600)
}

// Exchange swaps an authorization code for access+refresh tokens
// (game.AudioDevice), porting authorizationCodeGrant (server.js:1216-1220).
func (c *Client) Exchange(ctx context.Context, code string) error {
	_, err := c.ExchangeToken(ctx, code)
	return err
}

// ExchangeToken is like Exchange but also returns the access token so the HTTP
// handler can pass it to the Stage frontend for the Web Playback SDK.
func (c *Client) ExchangeToken(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.redirectURI)

	tok, err := c.postToken(ctx, form)
	if err != nil {
		return "", fmt.Errorf("spotify: exchange code: %w", err)
	}

	c.mu.Lock()
	c.accessToken = tok.AccessToken
	c.refreshToken = tok.RefreshToken
	c.expiresAt = c.expiryFrom(tok.ExpiresIn)
	c.mu.Unlock()
	return tok.AccessToken, nil
}

// clock returns the client's clock, defaulting to time.Now when unset (e.g.
// struct-literal construction in tests).
func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// expiryFrom converts a Spotify expires_in (seconds) into an absolute deadline.
// Defaults to 3600s if the field is missing/zero.
func (c *Client) expiryFrom(expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return c.clock().Add(time.Duration(expiresIn) * time.Second)
}

// refresh obtains a fresh access token using the stored refresh token,
// porting refreshSpotifyToken (server.js:2283-2300). The refresh grant keeps
// the existing refresh token unless Spotify rotates it.
func (c *Client) refresh(ctx context.Context) error {
	c.mu.Lock()
	rt := c.refreshToken
	c.mu.Unlock()
	if rt == "" {
		return fmt.Errorf("spotify: no refresh token; re-authenticate")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", rt)

	tok, err := c.postToken(ctx, form)
	if err != nil {
		return fmt.Errorf("spotify: refresh token: %w", err)
	}

	c.mu.Lock()
	c.accessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		c.refreshToken = tok.RefreshToken
	}
	c.expiresAt = c.expiryFrom(tok.ExpiresIn)
	c.mu.Unlock()
	return nil
}

// ValidToken returns a non-expired access token, refreshing first if the
// current one is missing or within the expiry skew window. This is what the
// stage's token endpoint serves so the Web Playback SDK's getOAuthToken
// callback always receives a live token, even hours into a game — Spotify
// access tokens die ~1h after issue regardless of activity (so a long game
// MUST refresh; switching tracks does not keep a token alive).
func (c *Client) ValidToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	tok := c.accessToken
	exp := c.expiresAt
	hasRefresh := c.refreshToken != ""
	c.mu.Unlock()

	// Refresh a bit early so an in-flight token never expires between the
	// callback fetch and the SDK using it.
	const skew = 2 * time.Minute
	if tok != "" && c.clock().Before(exp.Add(-skew)) {
		return tok, nil
	}
	if !hasRefresh {
		if tok != "" {
			return tok, nil // no way to refresh; hand back what we have
		}
		return "", fmt.Errorf("spotify: not authenticated")
	}
	if err := c.refresh(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	tok = c.accessToken
	c.mu.Unlock()
	return tok, nil
}

// postToken posts a form-encoded grant to the token endpoint with the client
// credentials as Basic auth (Spotify's accepted scheme for the token call).
func (c *Client) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(c.clientID, c.clientSecret)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint returned no access_token")
	}
	return &tok, nil
}

// Play starts playback of trackURI on the bound device at positionMs
// (game.AudioDevice). Ports the legacy play() call (server.js:1299) but routes
// explicitly to the stage's Virtual Device via device_id (design_doc §9)
// rather than relying on the implicit active device.
func (c *Client) Play(ctx context.Context, trackURI string, positionMs int64) error {
	bodyObj := map[string]any{
		"uris":        []string{trackURI},
		"position_ms": positionMs,
	}
	body, err := json.Marshal(bodyObj)
	if err != nil {
		return fmt.Errorf("spotify: marshal play body: %w", err)
	}
	return c.playerCommand(ctx, "play", body)
}

// Pause pauses playback on the bound device (game.AudioDevice), porting
// server.js:1449. Note (design_doc §9): the latency-critical pause-on-buzz is
// handled by the engine over WebTransport, not this API round-trip; this is
// the slower-path API pause used outside that hot path.
func (c *Client) Pause(ctx context.Context) error {
	return c.playerCommand(ctx, "pause", nil)
}

// Resume resumes playback on the bound device (game.AudioDevice). Legacy resume
// was a bare play() with no body (server.js:1481); we send no body so Spotify
// continues the current track from its paused position.
func (c *Client) Resume(ctx context.Context) error {
	return c.playerCommand(ctx, "play", nil)
}

// playerCommand issues PUT /v1/me/player/{action}?device_id=... with the
// bearer token, and on a 401 refreshes the token and retries exactly once —
// the control flow ported from the legacy play/pause handlers
// (server.js:1300-1311, 1481-1490).
func (c *Client) playerCommand(ctx context.Context, action string, body []byte) error {
	resp, err := c.doPlayer(ctx, action, body)
	if err != nil {
		return fmt.Errorf("spotify: %s: %w", action, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		// Token likely expired — refresh and retry once (legacy parity).
		resp.Body.Close()
		if rerr := c.refresh(ctx); rerr != nil {
			return fmt.Errorf("spotify: %s: %w", action, rerr)
		}
		resp, err = c.doPlayer(ctx, action, body)
		if err != nil {
			return fmt.Errorf("spotify: %s (after refresh): %w", action, err)
		}
	}
	defer resp.Body.Close()

	// Spotify player commands return 204 No Content on success; 202 can appear
	// while a device spins up. Anything else is an error.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("spotify: %s: unexpected status %d: %s", action, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// doPlayer builds and sends a single player PUT. Each call reads the current
// access token + device id under the lock so a mid-flight refresh is picked up
// on retry.
func (c *Client) doPlayer(ctx context.Context, action string, body []byte) (*http.Response, error) {
	c.mu.Lock()
	token := c.accessToken
	device := c.deviceID
	c.mu.Unlock()

	if token == "" {
		return nil, fmt.Errorf("not authenticated with Spotify")
	}

	endpoint := c.apiBase + "/me/player/" + action
	if device != "" {
		q := url.Values{}
		q.Set("device_id", device)
		endpoint += "?" + q.Encode()
	}

	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.HTTPClient.Do(req)
}
