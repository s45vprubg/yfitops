package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// SearchResult represents a single track from the Spotify Search API.
type SearchResult struct {
	URI        string `json:"uri"`
	Artist     string `json:"artist"`
	Song       string `json:"song"`
	AlbumArt   string `json:"albumArt"`
	DurationMs int64  `json:"durationMs"`
	Year       int    `json:"year"`     // parsed from album release_date (0 = unknown)
	Genre      string `json:"genre"`    // primary artist genre (filled by GetPlaylistTracks)
	artistID   string // primary artist ID, used to batch-fetch genres (unexported)
}

// Search queries the Spotify Search API for tracks matching query.
// Limit is clamped to 1-50 (Spotify's range). Uses the same bearer token and
// 401-refresh-retry pattern as playerCommand.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	resp, err := c.doSearch(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("spotify: search: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		if rerr := c.refresh(ctx); rerr != nil {
			return nil, fmt.Errorf("spotify: search: %w", rerr)
		}
		resp, err = c.doSearch(ctx, query, limit)
		if err != nil {
			return nil, fmt.Errorf("spotify: search (after refresh): %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("spotify: search: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("spotify: search: read body: %w", err)
	}

	var payload searchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("spotify: search: decode: %w", err)
	}

	results := make([]SearchResult, 0, len(payload.Tracks.Items))
	for _, item := range payload.Tracks.Items {
		artist := ""
		if len(item.Artists) > 0 {
			artist = item.Artists[0].Name
		}
		albumArt := ""
		if len(item.Album.Images) > 0 {
			albumArt = item.Album.Images[0].URL
		}
		results = append(results, SearchResult{
			URI:        item.URI,
			Artist:     artist,
			Song:       item.Name,
			AlbumArt:   albumArt,
			DurationMs: item.DurationMs,
		})
	}
	return results, nil
}

// GetPlaylistTracks fetches all tracks from a Spotify playlist via the
// /playlists/{id}/items endpoint (the /tracks endpoint was deprecated in the
// Feb-2026 Web API migration and 403s for Development-Mode custom OAuth
// clients). The playlistID can be extracted from URIs like
// "spotify:playlist:XXX" or URLs like "open.spotify.com/playlist/XXX".
func (c *Client) GetPlaylistTracks(ctx context.Context, playlistID string) ([]SearchResult, error) {
	// Ensure a live access token before the first request. On a cold start we
	// may hold only a restored refresh token (accessToken empty); ValidToken
	// mints one from it. Without this the first doPlaylistFetch bails with "not
	// authenticated" instead of refreshing (the retry path only fires on a 401
	// response, which we never reach with an empty token).
	if _, err := c.ValidToken(ctx); err != nil {
		return nil, fmt.Errorf("spotify: playlist: %w", err)
	}

	var all []SearchResult
	offset := 0
	limit := 100

	for {
		resp, err := c.doPlaylistFetch(ctx, playlistID, offset, limit)
		if err != nil {
			return nil, fmt.Errorf("spotify: playlist: %w", err)
		}

		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			if rerr := c.refresh(ctx); rerr != nil {
				return nil, fmt.Errorf("spotify: playlist: %w", rerr)
			}
			resp, err = c.doPlaylistFetch(ctx, playlistID, offset, limit)
			if err != nil {
				return nil, fmt.Errorf("spotify: playlist (after refresh): %w", err)
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("spotify: playlist: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("spotify: playlist: read body: %w", err)
		}

		var page playlistResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("spotify: playlist: decode: %w", err)
		}

		for _, item := range page.Items {
			if item.Track.URI == "" {
				continue
			}
			artist, artistID := "", ""
			if len(item.Track.Artists) > 0 {
				artist = item.Track.Artists[0].Name
				artistID = item.Track.Artists[0].ID
			}
			albumArt := ""
			if len(item.Track.Album.Images) > 0 {
				albumArt = item.Track.Album.Images[0].URL
			}
			all = append(all, SearchResult{
				URI:        item.Track.URI,
				Artist:     artist,
				Song:       item.Track.Name,
				AlbumArt:   albumArt,
				DurationMs: item.Track.DurationMs,
				Year:       yearFromReleaseDate(item.Track.Album.ReleaseDate),
				artistID:   artistID,
			})
		}

		if page.Next == "" {
			break
		}
		offset += limit
	}

	// Genre isn't on the track — it lives on the artist. Batch-fetch the primary
	// artists' genres and fill each result (best-effort; failures leave genre "").
	c.fillGenres(ctx, all)
	return all, nil
}

// yearFromReleaseDate parses the leading 4-digit year from a Spotify
// release_date ("YYYY", "YYYY-MM", "YYYY-MM-DD"). Returns 0 if unparseable.
func yearFromReleaseDate(rd string) int {
	if len(rd) < 4 {
		return 0
	}
	y, err := strconv.Atoi(rd[:4])
	if err != nil {
		return 0
	}
	return y
}

// fillGenres batch-fetches artist genres (Spotify allows up to 50 IDs per
// /artists call) and assigns each track its primary artist's first genre.
func (c *Client) fillGenres(ctx context.Context, tracks []SearchResult) {
	// Collect unique artist IDs.
	ids := make([]string, 0, len(tracks))
	seen := map[string]bool{}
	for i := range tracks {
		id := tracks[i].artistID
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}

	genreByArtist := map[string]string{}
	for start := 0; start < len(ids); start += 50 {
		end := start + 50
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		g, err := c.artistGenres(ctx, batch)
		if err != nil {
			continue // best-effort; leave these genres blank
		}
		for id, genre := range g {
			genreByArtist[id] = genre
		}
	}

	for i := range tracks {
		if genre, ok := genreByArtist[tracks[i].artistID]; ok {
			tracks[i].Genre = genre
		}
	}
}

// artistGenres returns artistID -> first genre for a batch of up to 50 IDs.
func (c *Client) artistGenres(ctx context.Context, ids []string) (map[string]string, error) {
	c.mu.Lock()
	token := c.accessToken
	c.mu.Unlock()
	if token == "" {
		return nil, fmt.Errorf("not authenticated with Spotify")
	}

	endpoint := c.apiBase + "/artists?ids=" + url.QueryEscape(strings.Join(ids, ","))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify: artists: status %d", resp.StatusCode)
	}
	var ar struct {
		Artists []struct {
			ID     string   `json:"id"`
			Genres []string `json:"genres"`
		} `json:"artists"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range ar.Artists {
		if len(a.Genres) > 0 {
			out[a.ID] = a.Genres[0]
		}
	}
	return out, nil
}

func (c *Client) doSearch(ctx context.Context, query string, limit int) (*http.Response, error) {
	c.mu.Lock()
	token := c.accessToken
	c.mu.Unlock()

	if token == "" {
		return nil, fmt.Errorf("not authenticated with Spotify")
	}

	q := url.Values{}
	q.Set("q", query)
	q.Set("type", "track")
	q.Set("limit", strconv.Itoa(limit))
	endpoint := c.apiBase + "/search?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	return c.HTTPClient.Do(req)
}

func (c *Client) doPlaylistFetch(ctx context.Context, playlistID string, offset, limit int) (*http.Response, error) {
	c.mu.Lock()
	token := c.accessToken
	c.mu.Unlock()

	if token == "" {
		return nil, fmt.Errorf("not authenticated with Spotify")
	}

	q := url.Values{}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("limit", strconv.Itoa(limit))
	// Feb-2026 Web API migration: the old /playlists/{id}/tracks endpoint is
	// deprecated and 403s for custom OAuth clients in Development Mode. The
	// replacement is /playlists/{id}/items, which nests the track one level
	// deeper (items[].item instead of items[].track). It also REQUIRES a market
	// (or user country) — without one Spotify treats the content as
	// unavailable — so we pass market=from_token to use the authed user's
	// country.
	q.Set("fields", "items(item(uri,name,duration_ms,artists(id,name),album(images(url),release_date))),next")
	q.Set("market", "from_token")
	endpoint := c.apiBase + "/playlists/" + playlistID + "/items?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	return c.HTTPClient.Do(req)
}

// Spotify API response types (unexported, used only for JSON decoding).

type searchResponse struct {
	Tracks struct {
		Items []searchTrackItem `json:"items"`
	} `json:"tracks"`
}

type searchTrackItem struct {
	URI        string         `json:"uri"`
	Name       string         `json:"name"`
	DurationMs int64          `json:"duration_ms"`
	Artists    []searchArtist `json:"artists"`
	Album      searchAlbum    `json:"album"`
}

type searchArtist struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type searchAlbum struct {
	Images      []searchImage `json:"images"`
	ReleaseDate string        `json:"release_date"` // "YYYY", "YYYY-MM", or "YYYY-MM-DD"
}

type searchImage struct {
	URL string `json:"url"`
}

type playlistResponse struct {
	Items []playlistItem `json:"items"`
	Next  string         `json:"next"`
}

// playlistItem matches the /playlists/{id}/items response (Feb-2026 migration):
// each entry nests the track under "item" rather than the old "track" key.
type playlistItem struct {
	Track searchTrackItem `json:"item"`
}
