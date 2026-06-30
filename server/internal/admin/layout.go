package admin

import (
	"net/http"
	"strconv"
)

func (h *Handler) getLayout(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	layout, err := h.store.GetLayout(r.Context(), boardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if layout == nil {
		http.Error(w, "board not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, layout)
}

func (h *Handler) addColumn(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")

	var body struct {
		Category string `json:"category"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Category == "" {
		http.Error(w, "category is required", http.StatusBadRequest)
		return
	}
	if len(body.Category) > 100 {
		http.Error(w, "category too long (max 100)", http.StatusBadRequest)
		return
	}

	board, err := h.store.GetBoard(r.Context(), boardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if board == nil {
		http.Error(w, "board not found", http.StatusNotFound)
		return
	}
	if board.Cols >= 8 {
		http.Error(w, "maximum 8 columns", http.StatusConflict)
		return
	}

	newCol := board.Cols + 1
	if err := h.store.AddColumn(r.Context(), boardID, newCol, body.Category); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateBoardCols(r.Context(), boardID, newCol); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"col": newCol, "category": body.Category})
}

func (h *Handler) removeColumn(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	col, err := strconv.Atoi(r.PathValue("col"))
	if err != nil || col < 1 {
		http.Error(w, "invalid column number", http.StatusBadRequest)
		return
	}

	board, err := h.store.GetBoard(r.Context(), boardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if board == nil {
		http.Error(w, "board not found", http.StatusNotFound)
		return
	}
	if board.Cols <= 1 {
		http.Error(w, "cannot remove the last column", http.StatusConflict)
		return
	}

	if err := h.store.RemoveColumn(r.Context(), boardID, col); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateBoardCols(r.Context(), boardID, board.Cols-1); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) renameCategory(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	col, err := strconv.Atoi(r.PathValue("col"))
	if err != nil || col < 1 {
		http.Error(w, "invalid column number", http.StatusBadRequest)
		return
	}

	var body struct {
		Category string `json:"category"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Category == "" {
		http.Error(w, "category is required", http.StatusBadRequest)
		return
	}

	if err := h.store.RenameCategory(r.Context(), boardID, col, body.Category); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) placeTrack(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	row, err := strconv.Atoi(r.PathValue("row"))
	if err != nil || row < 1 || row > 5 {
		http.Error(w, "row must be 1-5", http.StatusBadRequest)
		return
	}
	col, cerr := strconv.Atoi(r.PathValue("col"))
	if cerr != nil || col < 1 || col > 8 {
		http.Error(w, "col must be 1-8", http.StatusBadRequest)
		return
	}
	trackID := r.PathValue("trackId")

	var body struct {
		Pos int `json:"pos"`
	}
	if r.ContentLength > 0 {
		_ = decodeJSON(r, &body)
	}

	if err := h.store.PlaceTrack(r.Context(), boardID, row, col, trackID, body.Pos); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) unplaceTrack(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	row, err := strconv.Atoi(r.PathValue("row"))
	if err != nil || row < 1 || row > 5 {
		http.Error(w, "row must be 1-5", http.StatusBadRequest)
		return
	}
	col, cerr := strconv.Atoi(r.PathValue("col"))
	if cerr != nil || col < 1 || col > 8 {
		http.Error(w, "col must be 1-8", http.StatusBadRequest)
		return
	}
	trackID := r.PathValue("trackId")

	if err := h.store.UnplaceTrack(r.Context(), boardID, row, col, trackID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) attachBoard(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")

	var body struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.SessionID == "" {
		http.Error(w, "sessionId is required", http.StatusBadRequest)
		return
	}

	if err := h.store.AttachBoard(r.Context(), body.SessionID, boardID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	board, err := h.store.LoadBoardByID(r.Context(), boardID)
	if err != nil {
		http.Error(w, "attached but failed to load: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.engine.ReloadBoard(board)
	writeJSON(w, http.StatusOK, map[string]string{"status": "attached"})
}

func (h *Handler) startGame(w http.ResponseWriter, r *http.Request) {
	if err := h.engine.StartGame(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (h *Handler) resetGame(w http.ResponseWriter, r *http.Request) {
	if err := h.engine.ResetToLobby(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}
