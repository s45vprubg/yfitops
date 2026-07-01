package admin

import (
	"net/http"
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Keep at most `cols` categories; drop unknown/duplicate track IDs.
	cats := proposal.Categories
	if len(cats) > cols {
		cats = cats[:cols]
	}
	placed := map[string]bool{}

	// Clear the existing layout so we start clean (remove all current columns).
	if layout, lerr := h.store.GetLayout(r.Context(), boardID); lerr == nil && layout != nil {
		seen := map[int]bool{}
		for _, c := range layout.Cells {
			if !seen[c.Col] {
				seen[c.Col] = true
				_ = h.store.RemoveColumn(r.Context(), boardID, c.Col)
			}
		}
	}

	_ = h.store.UpdateBoardCols(r.Context(), boardID, len(cats))

	applied := 0
	for ci, cat := range cats {
		col := ci + 1
		if err := h.store.AddColumn(r.Context(), boardID, col, cat.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row := 1
		for _, tid := range cat.TrackIDs {
			if row > rows || !byID[tid] || placed[tid] {
				continue // full column, unknown id, or already placed elsewhere
			}
			if err := h.store.PlaceTrack(r.Context(), boardID, row, col, tid, row-1); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			placed[tid] = true
			applied++
			row++
		}
	}

	writeJSON(w, http.StatusOK, map[string]int{
		"categories": len(cats),
		"placed":     applied,
		"total":      len(tracks),
	})
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
