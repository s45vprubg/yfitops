package game

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/anticheat"
	"github.com/s45vprubg/yfitops/server/internal/protocol"
)

// fakeLyricsStore is a controllable game.LyricsTextStore for testing the
// engine's DB-first lyric lookup.
type fakeLyricsStore struct {
	resolved map[string]string // spotifyURI -> raw LRC text
}

func (f *fakeLyricsStore) GetResolvedLyrics(_ context.Context, spotifyURI string) (string, bool, error) {
	if text, ok := f.resolved[spotifyURI]; ok {
		return text, true, nil
	}
	return "", false, nil
}

// explodingLyrics is a LyricsProvider that fails the test if ever called —
// used to prove the DB-first path never falls through to a live fetch when
// the song is already resolved in the store.
type explodingLyrics struct{ t *testing.T }

func (e explodingLyrics) Fetch(context.Context, string, string, int) ([]protocol.LyricLine, error) {
	e.t.Error("LyricsProvider.Fetch called despite a resolved DB entry — DB-first lookup was bypassed")
	return nil, errors.New("should not be called")
}

func newLyricsTestEngine(t *testing.T, lp LyricsProvider, ls LyricsTextStore) *Engine {
	t.Helper()
	bc := newFakeBcast()
	lk := newFakeLock()
	repo := newFakeRepo()
	audio := &fakeAudio{}
	gate := anticheat.NewNonceGate([]byte("test-secret"))
	return NewEngine(repo, lk, audio, lp, ls, bc, gate, Config{
		SessionID:    "s1",
		BuzzWindowMs: -1,
		Rand:         rand.New(rand.NewSource(1)),
	})
}

// TestLyricFetch_DBHitSkipsLiveFetch is the core regression test for the
// play-time DB-first lookup: when the global lyrics cache already has a
// resolved entry for a song's spotifyUri, the engine must serve it directly
// and must NEVER call the live LRCLIB provider — removing the runtime
// dependency on LRCLIB for any song already resolved during board building.
func TestLyricFetch_DBHitSkipsLiveFetch(t *testing.T) {
	store := &fakeLyricsStore{resolved: map[string]string{
		"spotify:track:cached": "[00:01.00]cached line one\n[00:02.00]cached line two",
	}}
	e := newLyricsTestEngine(t, explodingLyrics{t: t}, store)

	ent := e.lyricFetch("spotify:track:cached", "Artist", "Song", 180)
	select {
	case <-ent.done:
	case <-time.After(2 * time.Second):
		t.Fatal("lyricFetch did not complete")
	}

	if ent.err != nil {
		t.Fatalf("ent.err = %v, want nil", ent.err)
	}
	want := []protocol.LyricLine{{TimeMs: 1000, Text: "cached line one"}, {TimeMs: 2000, Text: "cached line two"}}
	if len(ent.lines) != len(want) {
		t.Fatalf("lines = %#v, want %#v", ent.lines, want)
	}
	for i := range want {
		if ent.lines[i] != want[i] {
			t.Fatalf("lines[%d] = %#v, want %#v", i, ent.lines[i], want[i])
		}
	}
}

// TestLyricFetch_DBMissFallsBackToLiveFetch covers the other half: a song
// with no resolved DB entry must still fall back to the live provider exactly
// as it did before the cache existed (DB-first, not DB-only).
func TestLyricFetch_DBMissFallsBackToLiveFetch(t *testing.T) {
	store := &fakeLyricsStore{resolved: map[string]string{}}
	live := fakeLyrics{} // returns a canned line unconditionally
	e := newLyricsTestEngine(t, live, store)

	ent := e.lyricFetch("spotify:track:not-cached", "Artist", "Song", 180)
	select {
	case <-ent.done:
	case <-time.After(2 * time.Second):
		t.Fatal("lyricFetch did not complete")
	}

	if ent.err != nil {
		t.Fatalf("ent.err = %v, want nil", ent.err)
	}
	if len(ent.lines) != 1 || ent.lines[0].Text != "secret lyric line" {
		t.Fatalf("lines = %#v, want the live fakeLyrics fallback result", ent.lines)
	}
}

// TestLyricFetch_NilStoreFallsBackToLiveFetch covers the nil-safety: no
// Postgres (e.g. in-memory repo mode) must behave exactly like pre-cache
// code — always live fetch, never a DB lookup.
func TestLyricFetch_NilStoreFallsBackToLiveFetch(t *testing.T) {
	e := newLyricsTestEngine(t, fakeLyrics{}, nil)

	ent := e.lyricFetch("spotify:track:whatever", "Artist", "Song", 180)
	select {
	case <-ent.done:
	case <-time.After(2 * time.Second):
		t.Fatal("lyricFetch did not complete")
	}
	if ent.err != nil || len(ent.lines) != 1 {
		t.Fatalf("ent = %#v, want the live fakeLyrics fallback result with no error", ent)
	}
}
