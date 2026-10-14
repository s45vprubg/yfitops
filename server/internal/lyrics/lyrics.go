// Package lyrics implements the LRCLIB synced-lyrics client (design_doc §6).
//
// It satisfies game.LyricsProvider: given an artist/title/duration it queries
// LRCLIB's GET /api/get endpoint, which matches on artist + track + duration
// and returns LRC-format synced lyrics. The LRC text is parsed into absolute
// time-coded protocol.LyricLine values for the stage's karaoke overlay.
//
// Karaoke is non-essential: when LRCLIB has no synced lyrics (404 or an empty
// syncedLyrics field) Fetch returns (nil, nil) so the game simply shows none.
//
// Request shape and error handling follow LRCLIB's published API docs
// (https://lrclib.net/docs):
//   - a descriptive User-Agent is required ("we require you to identify your
//     client in requests"),
//   - 404 TrackNotFound means LRCLIB confirmed no match (not an error),
//   - 429 (rate limited) and 503 ServerOverloaded are transient and carry a
//     Retry-After header clients "must honor",
//   - album_name is deliberately omitted from the request even though LRCLIB
//     recommends it for match precision — accepted tradeoff, see
//     docs/CHANGELOG.md.
package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/config"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// HTTPClient is the minimal slice of *http.Client the client needs, kept as an
// interface so tests can inject an httptest-backed transport without the
// network.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// LRCLIBClient fetches synced lyrics from an LRCLIB-compatible API.
type LRCLIBClient struct {
	baseURL string
	http    HTTPClient
}

// userAgent identifies this client per LRCLIB's documented requirement
// ("set the User-Agent header with your application's name, version, and a
// link to its homepage/project page or an email address").
const userAgent = "yfitops/1.0 (+https://github.com/s45vprubg/yfitops)"

// New builds an LRCLIBClient from config (design_doc §6). The base URL comes
// from cfg.LRCLIBBaseURL; the HTTP client is the stdlib default and can be
// swapped via WithHTTPClient for tests.
func New(cfg *config.Config) *LRCLIBClient {
	return &LRCLIBClient{
		baseURL: strings.TrimRight(cfg.LRCLIBBaseURL, "/"),
		// A bounded timeout so a slow/hung LRCLIB call can't stall a karaoke
		// lyric fetch or wedge a bulk lyrics rescan worker indefinitely.
		http: &http.Client{Timeout: 8 * time.Second},
	}
}

// WithHTTPClient overrides the HTTP client (used by tests). Returns the
// receiver for chaining.
func (c *LRCLIBClient) WithHTTPClient(h HTTPClient) *LRCLIBClient {
	c.http = h
	return c
}

// maxLyricsBytes caps how much of an LRCLIB response we buffer, bounding memory
// against a hostile/oversized body. A body at/over the cap will simply fail the
// JSON decode cleanly.
const maxLyricsBytes = 1 << 20

// lrclibResponse is the subset of the LRCLIB /api/get payload we care about.
type lrclibResponse struct {
	SyncedLyrics string `json:"syncedLyrics"`
}

// RateLimitError is returned by probeOnce on a 429/503 response. RetryAfter is
// parsed from the documented Retry-After header (seconds), defaulting to 1s
// per the docs ("usually 1 second" for 503) when the header is absent/invalid.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("lyrics: rate limited, retry after %s", e.RetryAfter)
}

// maxRetryWait bounds how long a single Probe/Fetch call will sleep for a
// Retry-After value before giving up — a misbehaving/huge Retry-After must
// not hang a bulk rescan on one track indefinitely.
const maxRetryWait = 10 * time.Second

// Fetch implements game.LyricsProvider. It returns time-coded lyric lines for
// the track, or (nil, nil) when no synced lyrics are available.
func (c *LRCLIBClient) Fetch(ctx context.Context, artist, title string, durationSec int) ([]protocol.LyricLine, error) {
	text, found, err := c.Probe(ctx, artist, title, durationSec)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return ParseLRC(text), nil
}

// Probe fetches raw LRC text for a track and classifies the result per
// LRCLIB's documented status codes:
//   - found=false, err=nil: LRCLIB confirmed no lyrics exist (404 TrackNotFound,
//     or a match with an empty syncedLyrics field).
//   - err is a *RateLimitError: rate limited (429) or temporarily overloaded
//     (503). Probe already waited out one Retry-After and retried once before
//     returning this, so callers that still see it should back off further
//     rather than retrying immediately.
//   - err is any other error: transport failure, unexpected status, or a
//     malformed response body — a genuine, non-retried-here failure.
func (c *LRCLIBClient) Probe(ctx context.Context, artist, song string, durationSec int) (lrcText string, found bool, err error) {
	text, found, err := c.probeOnce(ctx, artist, song, durationSec)
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		return text, found, err
	}

	wait := rle.RetryAfter
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	select {
	case <-time.After(wait):
	case <-ctx.Done():
		return "", false, fmt.Errorf("lyrics: retry wait: %w", ctx.Err())
	}
	return c.probeOnce(ctx, artist, song, durationSec)
}

// probeOnce makes a single /api/get request with no retry logic.
func (c *LRCLIBClient) probeOnce(ctx context.Context, artist, song string, durationSec int) (string, bool, error) {
	q := url.Values{}
	q.Set("artist_name", artist)
	q.Set("track_name", song)
	q.Set("duration", strconv.Itoa(durationSec))
	endpoint := c.baseURL + "/api/get?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, fmt.Errorf("lyrics: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("lyrics: request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound:
		// LRCLIB confirmed no match -> karaoke just shows nothing.
		return "", false, nil
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		// Docs: "the response body is not guaranteed to be JSON" for these —
		// rely on the status code + Retry-After header only, never the body.
		return "", false, &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case http.StatusOK:
		// fall through to body parsing below
	default:
		return "", false, fmt.Errorf("lyrics: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLyricsBytes))
	if err != nil {
		return "", false, fmt.Errorf("lyrics: read body: %w", err)
	}

	var lr lrclibResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return "", false, fmt.Errorf("lyrics: decode body: %w", err)
	}
	if strings.TrimSpace(lr.SyncedLyrics) == "" {
		return "", false, nil
	}

	return lr.SyncedLyrics, true, nil
}

// parseRetryAfter parses the Retry-After header (seconds, per RFC 7231 /
// LRCLIB's docs) and falls back to 1s — the docs' own stated default for a
// 503 — when the header is missing or not a valid non-negative integer.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	// Bound secs before multiplying by time.Second: a pathologically large
	// value (e.g. a corrupted/malicious ~10-digit header) can overflow the
	// int64 multiply and wrap to a negative duration, which would then evade
	// the caller's `wait > maxRetryWait` clamp (a negative is never greater
	// than maxRetryWait). Clamping the input here, pre-multiply, closes that
	// window entirely regardless of what the caller does afterward.
	if err != nil || secs < 0 || secs > int(maxRetryWait/time.Second) {
		return 1 * time.Second
	}
	return time.Duration(secs) * time.Second
}

// tsRe matches a single LRC timestamp tag: [mm:ss.xx] or [mm:ss.xxx], with the
// fractional part optional ([mm:ss]). Minutes/seconds may be one or more
// digits to tolerate sloppy producers.
var tsRe = regexp.MustCompile(`\[(\d+):(\d{1,2})(?:[.:](\d{1,3}))?\]`)

// ParseLRC converts an LRC document into absolute-time lyric lines.
//
// It handles:
//   - one or many leading timestamps per line (the same text emitted at each),
//   - [mm:ss.xx] hundredths and [mm:ss.xxx] millisecond precision,
//   - blank lines and timestamp-only lines (emitted as empty text, useful as
//     karaoke "clear" cues),
//   - metadata tags like [ar:], [ti:], [length:] which carry no timestamp and
//     are skipped.
//
// Lines without any timestamp tag are dropped. The result is sorted by TimeMs.
func ParseLRC(doc string) []protocol.LyricLine {
	var out []protocol.LyricLine

	for _, raw := range strings.Split(doc, "\n") {
		line := strings.TrimRight(raw, "\r")

		// Collect all timestamp tags at the start of the line. LRC puts every
		// timestamp before the text; we strip them off the front in order.
		var times []int64
		rest := line
		for {
			loc := tsRe.FindStringSubmatchIndex(rest)
			// Only consume tags anchored at the current head of the string;
			// once we hit text, the remainder is the lyric.
			if loc == nil || loc[0] != 0 {
				break
			}
			groups := tsRe.FindStringSubmatch(rest[loc[0]:loc[1]])
			times = append(times, lrcTimeMs(groups[1], groups[2], groups[3]))
			rest = rest[loc[1]:]
		}

		if len(times) == 0 {
			// No timestamp: metadata tag ([ar:]/[ti:]/...) or plain text. Skip.
			continue
		}

		text := strings.TrimSpace(rest)
		for _, t := range times {
			out = append(out, protocol.LyricLine{TimeMs: t, Text: text})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].TimeMs < out[j].TimeMs })
	return out
}

// lrcTimeMs converts the captured minute/second/fraction groups into absolute
// milliseconds. frac is the raw digits after the separator (1-3 of them):
// "4" -> 400ms, "40" -> 400ms, "400" -> 400ms. Inputs are pre-validated by the
// regexp so the Atoi calls cannot fail.
func lrcTimeMs(minStr, secStr, frac string) int64 {
	min, _ := strconv.Atoi(minStr)
	sec, _ := strconv.Atoi(secStr)

	ms := 0
	switch len(frac) {
	case 0:
		ms = 0
	case 1:
		ms, _ = strconv.Atoi(frac)
		ms *= 100
	case 2:
		ms, _ = strconv.Atoi(frac)
		ms *= 10
	case 3:
		ms, _ = strconv.Atoi(frac)
	}

	return int64(min)*60_000 + int64(sec)*1_000 + int64(ms)
}
