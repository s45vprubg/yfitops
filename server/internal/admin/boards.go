package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

func (h *Handler) listBoards(w http.ResponseWriter, r *http.Request) {
	boards, err := h.store.ListBoards(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, boards)
}

func (h *Handler) createBoard(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(body.Name) > 200 {
		http.Error(w, "name too long (max 200)", http.StatusBadRequest)
		return
	}

	id := generateID("brd")
	if err := h.store.CreateBoard(r.Context(), id, body.Name); err != nil {
		serverError(w, err)
		return
	}
	if err := h.store.AddColumn(r.Context(), id, 1, ""); err != nil {
		serverError(w, err)
		return
	}

	board, err := h.store.GetBoard(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, board)
}

func (h *Handler) getBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	board, err := h.store.GetBoard(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	if board == nil {
		http.Error(w, "board not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (h *Handler) renameBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(body.Name) > 200 {
		http.Error(w, "name too long (max 200)", http.StatusBadRequest)
		return
	}

	if err := h.store.RenameBoard(r.Context(), id, body.Name); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteBoard(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func generateID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
