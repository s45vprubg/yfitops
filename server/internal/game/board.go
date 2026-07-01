package game

import (
	"math/rand"

	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// board.go implements the Jeopardy grid selection and multi-track cell pool
// logic (design_doc §7). A cell holds 4-6 tracks; selecting it plays a random
// unplayed track and the cell stays live until its whole pool is exhausted.

// cellAt returns the cell at the 1-indexed (row,col), or nil if out of range.
func cellAt(b *Board, row, col int) *Cell {
	if b == nil {
		return nil
	}
	for _, rowCells := range b.Cells {
		for _, c := range rowCells {
			if c != nil && c.Row == row && c.Col == col {
				return c
			}
		}
	}
	return nil
}

// pickTrack selects a random unplayed track from a cell's pool (§7). It does
// NOT mark the track played — the engine marks it played only once the cell is
// fully resolved, so an abandoned round can replay it. Returns nil if the pool
// is exhausted. rng is injected so tests are deterministic.
func pickTrack(c *Cell, rng *rand.Rand) *Track {
	if c == nil {
		return nil
	}
	pool := make([]*Track, 0, len(c.Tracks))
	for _, t := range c.Tracks {
		// Skip played tracks and lyric-less tracks (unless admin-overridden):
		// karaoke needs words, so a track without synced lyrics is not
		// auto-selected by default.
		if !t.Played && t.Playable {
			pool = append(pool, t)
		}
	}
	if len(pool) == 0 {
		return nil
	}
	if rng == nil {
		return pool[0]
	}
	return pool[rng.Intn(len(pool))]
}

// boardData serializes the grid for the trusted stage/admin board view. It
// reports per-cell points (the row ceiling), exhausted flag, and tracks left.
// Categories/points are not secret; track metadata is never included here.
func boardData(b *Board) protocol.BoardData {
	if b == nil {
		return protocol.BoardData{}
	}
	cells := make([]protocol.BoardCell, 0, b.Rows*b.Cols)
	for _, rowCells := range b.Cells {
		for _, c := range rowCells {
			if c == nil {
				continue
			}
			cells = append(cells, protocol.BoardCell{
				Row:        c.Row,
				Col:        c.Col,
				Category:   c.Category,
				Points:     MaxPointsForRow(c.Row),
				Exhausted:  c.Exhausted(),
				TracksLeft: c.TracksLeft(),
			})
		}
	}
	return protocol.BoardData{Rows: b.Rows, Cols: b.Cols, Cells: cells}
}
