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

// GetPlaylistTracks fetches all tracks from a Spotify playlist. The playlistID
// can be extracted from URIs like "spotify:playlist:XXX" or URLs like
// "open.spotify.com/playlist/XXX".
func (c *Client) GetPlaylistTracks(ctx context.Context, playlistID string) ([]SearchResult, error) {
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
			artist := ""
			if len(item.Track.Artists) > 0 {
				artist = item.Track.Artists[0].Name
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
			})
		}

		if page.Next == "" {
			break
		}
		offset += limit
	}
	return all, nil
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
	q.Set("fields", "items(track(uri,name,duration_ms,artists(name),album(images(url)))),next")
	endpoint := c.apiBase + "/playlists/" + playlistID + "/tracks?" + q.Encode()

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
	Name string `json:"name"`
}

type searchAlbum struct {
	Images []searchImage `json:"images"`
}

type searchImage struct {
	URL string `json:"url"`
}

type playlistResponse struct {
	Items []playlistItem `json:"items"`
	Next  string         `json:"next"`
}

type playlistItem struct {
	Track searchTrackItem `json:"track"`
}
