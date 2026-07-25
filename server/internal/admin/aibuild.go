package admin

import (
	"log"
	"net/http"
	"strings"
)

// aiBuild uses the AI categorizer to lay out a board from its track library:
// it proposes ~6 themed categories (standard Jeopardy width) of 5 songs each,
// then APPLIES the plan — sets the board columns, creates/renames each category
// column, and places the assigned tracks (up to 5 per column, one per row). The
// admin can then tweak placements manually in the builder.
func (h *Handler) aiBuild(w http.ResponseWriter, r *http.Request) {
	if h.ai == nil {
		http.Error(w, "AI builder not configured (set GEMINI_API_KEY)", http.StatusServiceUnavailable)
		return
	}
	boardID := r.PathValue("id")

	// Optional cols override; default to a standard Jeopardy 6-wide board. Rows
	// are fixed at 5 (scoring contract).
	const rows = 5
	cols := 6
	if q := r.URL.Query().Get("cols"); q != "" {
		if n := atoiClamp(q, 1, 8); n > 0 {
			cols = n
		}
	}

	tracks, err := h.store.ListTracks(r.Context(), boardID)
	if err != nil {
		log.Printf("admin: aiBuild list tracks board=%s: %v", boardID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(tracks) == 0 {
		http.Error(w, "board has no tracks to categorize", http.StatusBadRequest)
		return
	}

	// Hand the model minimal track info.
	in := make([]AITrack, len(tracks))
	byID := make(map[string]bool, len(tracks))
	for i, t := range tracks {
		in[i] = AITrack{ID: t.ID, Artist: t.Artist, Song: t.Song}
		byID[t.ID] = true
	}

	proposal, err := h.ai.BuildCategories(r.Context(), in, rows, cols)
	if err != nil {
		// The upstream error can contain sensitive detail (e.g. request URLs);
		// log it server-side and return a generic message to the client.
		log.Printf("admin: aiBuild categorize board=%s: %v", boardID, err)
		http.Error(w, "AI build failed", http.StatusBadGateway)
		return
	}

	// Keep at most `cols` categories; drop unknown/duplicate track IDs.
	cats := proposal.Categories
	if len(cats) > cols {
		cats = cats[:cols]
	}
	placed := map[string]bool{}

	// Build the new layout in memory (applying the same row/byID/placed/dup
	// guards that previously gated PlaceTrack), then swap it in atomically so a
	// failure can't leave a corrupt half-built board.
	columns := make([]LayoutColumn, 0, len(cats))
	applied := 0
	for ci, cat := range cats {
		col := ci + 1
		lc := LayoutColumn{Category: sanitizeCategory(cat.Name)}
		row := 1
		for _, tid := range cat.TrackIDs {
			if row > rows || !byID[tid] || placed[tid] {
				continue // full column, unknown id, or already placed elsewhere
			}
			lc.Placements = append(lc.Placements, LayoutPlacement{
				Row: row, Col: col, TrackID: tid, Pos: row - 1,
			})
			placed[tid] = true
			applied++
			row++
		}
		columns = append(columns, lc)
	}

	if err := h.store.RebuildLayout(r.Context(), boardID, len(cats), columns); err != nil {
		log.Printf("admin: aiBuild rebuild layout board=%s: %v", boardID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]int{
		"categories": len(cats),
		"placed":     applied,
		"total":      len(tracks),
	})
}

// sanitizeCategory caps a proposed (AI-generated, attacker-influenceable)
// category name to 100 chars and strips control characters, matching the
// addColumn HTTP handler's 100-char cap.
func sanitizeCategory(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	return name
}

// atoiClamp parses a small positive int within [lo,hi]; returns 0 on failure.
func atoiClamp(s string, lo, hi int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > hi {
			return hi
		}
	}
	if n < lo {
		return 0
	}
	return n
}
