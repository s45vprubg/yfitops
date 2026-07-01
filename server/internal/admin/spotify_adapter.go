package admin

import (
	"context"

	"github.com/s45vprubg/yfitops/server/internal/spotify"
)

// SpotifyAdapter wraps *spotify.Client to satisfy the SpotifySearcher interface.
type SpotifyAdapter struct {
	Client *spotify.Client
}

// ValidToken returns a live Spotify access token, refreshing if near expiry.
func (a *SpotifyAdapter) ValidToken(ctx context.Context) (string, error) {
	return a.Client.ValidToken(ctx)
}

func (a *SpotifyAdapter) Search(ctx context.Context, query string, limit int) ([]SpotifyResult, error) {
	results, err := a.Client.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SpotifyResult, len(results))
	for i, r := range results {
		out[i] = SpotifyResult{
			URI:        r.URI,
			Artist:     r.Artist,
			Song:       r.Song,
			AlbumArt:   r.AlbumArt,
			DurationMs: r.DurationMs,
			Year:       r.Year,
			Genre:      r.Genre,
		}
	}
	return out, nil
}

func (a *SpotifyAdapter) GetPlaylistTracks(ctx context.Context, playlistID string) ([]SpotifyResult, error) {
	results, err := a.Client.GetPlaylistTracks(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	out := make([]SpotifyResult, len(results))
	for i, r := range results {
		out[i] = SpotifyResult{
			URI:        r.URI,
			Artist:     r.Artist,
			Song:       r.Song,
			AlbumArt:   r.AlbumArt,
			DurationMs: r.DurationMs,
			Year:       r.Year,
			Genre:      r.Genre,
		}
	}
	return out, nil
}
