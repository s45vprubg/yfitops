// Package ai wraps the Gemini generateContent API for the board builder's
// "build with AI" feature: given a list of tracks it proposes Jeopardy-style
// category buckets and assigns each track to one.
//
// It is intentionally small and dependency-free (stdlib net/http + json). The
// API key comes from GEMINI_API_KEY; the model defaults to gemini-3.1-flash-lite
// (overridable via GEMINI_MODEL). If the key is absent the client is nil and the
// admin endpoint returns 503 — the feature is optional.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultModel = "gemini-3.1-flash-lite"
	apiBase      = "https://generativelanguage.googleapis.com/v1beta/models/"
)

// Client calls Gemini generateContent.
type Client struct {
	apiKey string
	model  string
	http   *http.Client
}

// New returns a Client, or nil if apiKey is empty (feature disabled).
func New(apiKey, model string) *Client {
	if apiKey == "" {
		return nil
	}
	if model == "" {
		model = defaultModel
	}
	return &Client{apiKey: apiKey, model: model, http: &http.Client{Timeout: 60 * time.Second}}
}

// TrackInput is the minimal track info the model needs to categorize.
type TrackInput struct {
	ID     string `json:"id"`
	Artist string `json:"artist"`
	Song   string `json:"song"`
}

// Category is one proposed bucket with the track IDs assigned to it.
type Category struct {
	Name     string   `json:"name"`
	TrackIDs []string `json:"trackIds"`
}

// Proposal is the AI's board plan.
type Proposal struct {
	Categories []Category `json:"categories"`
}

// --- Gemini request/response shapes (subset) ---

type geminiReq struct {
	Contents         []geminiContent   `json:"contents"`
	GenerationConfig *geminiGenConfig  `json:"generationConfig,omitempty"`
}
type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct {
	Text string `json:"text"`
}
type geminiGenConfig struct {
	ResponseMimeType string `json:"responseMimeType,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
}
type geminiResp struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// BuildCategories asks Gemini to group the tracks into `cols` categories of
// roughly `rows` songs each, returning the parsed proposal. Every returned
// track ID is validated against the input set by the caller.
func (c *Client) BuildCategories(ctx context.Context, tracks []TrackInput, rows, cols int) (*Proposal, error) {
	prompt := buildPrompt(tracks, rows, cols)
	temp := 0.4
	reqBody := geminiReq{
		Contents: []geminiContent{{Parts: []geminiPart{{Text: prompt}}}},
		GenerationConfig: &geminiGenConfig{
			ResponseMimeType: "application/json",
			Temperature:      &temp,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}

	url := apiBase + c.model + ":generateContent?key=" + c.apiKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ai: request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var gr geminiResp
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("ai: decode response: %w", err)
	}
	if gr.Error != nil {
		return nil, fmt.Errorf("ai: gemini error: %s", gr.Error.Message)
	}
	if len(gr.Candidates) == 0 || len(gr.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("ai: empty response (status %d)", resp.StatusCode)
	}

	text := gr.Candidates[0].Content.Parts[0].Text
	// The model returns JSON (responseMimeType=json), but strip any stray code
	// fences just in case.
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")

	var p Proposal
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		return nil, fmt.Errorf("ai: parse proposal JSON: %w", err)
	}
	if len(p.Categories) == 0 {
		return nil, fmt.Errorf("ai: model returned no categories")
	}
	return &p, nil
}

func buildPrompt(tracks []TrackInput, rows, cols int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are curating a music trivia game board (Jeopardy-style).
Group the songs below into exactly %d themed categories, each with about %d songs.
Choose fun, specific category themes (genre, era, mood, artist trait, lyrical
theme, one-hit-wonders, etc.) — avoid generic names like "Category 1". Every
song must be placed in exactly one category. Balance the categories in size.

Return ONLY JSON of this exact shape (no prose):
{"categories":[{"name":"<theme>","trackIds":["<id>","<id>"]}]}

Songs (id — artist — title):
`, cols, rows)
	for _, t := range tracks {
		fmt.Fprintf(&b, "%s — %s — %s\n", t.ID, t.Artist, t.Song)
	}
	return b.String()
}
