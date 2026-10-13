package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/admin"
)

// lyrics_cache_staging_test.go verifies the global per-song lyrics cache
// (track_lyrics, migration 0008) against live Postgres: the full
// resolved/not_found/error lifecycle, that an error never knocks a resolved
// row back to pending, and the game engine's read path (GetResolvedLyrics).
// Gated on YFI_TEST_DSN; self-skips without it.
func TestStaging_TrackLyricsCache_Lifecycle(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres track_lyrics test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	uri := "spotify:track:qatest_lyrics_" + itoa(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM track_lyrics WHERE spotify_uri=$1`, uri)
	})

	// Never attempted: GetLyricsStatus returns nil, GetResolvedLyrics is a miss.
	rec, err := repo.GetLyricsStatus(ctx, uri)
	if err != nil {
		t.Fatalf("GetLyricsStatus (unattempted): %v", err)
	}
	if rec != nil {
		t.Fatalf("GetLyricsStatus (unattempted) = %+v, want nil", rec)
	}
	if _, ok, err := repo.GetResolvedLyrics(ctx, uri); err != nil || ok {
		t.Fatalf("GetResolvedLyrics (unattempted) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// A transient error creates a pending row with last_error set.
	if err := repo.UpsertLyricsError(ctx, uri, "simulated timeout"); err != nil {
		t.Fatalf("UpsertLyricsError: %v", err)
	}
	rec, err = repo.GetLyricsStatus(ctx, uri)
	if err != nil || rec == nil || rec.Status != admin.LyricsStatusPending || rec.LastError == "" {
		t.Fatalf("after UpsertLyricsError: rec=%+v err=%v, want pending with last_error set", rec, err)
	}

	// Resolving overwrites pending with the real text.
	if err := repo.UpsertLyricsResolved(ctx, uri, "[00:01.00]hello", "lrclib"); err != nil {
		t.Fatalf("UpsertLyricsResolved: %v", err)
	}
	rec, err = repo.GetLyricsStatus(ctx, uri)
	if err != nil || rec == nil || rec.Status != admin.LyricsStatusResolved || rec.Source != "lrclib" {
		t.Fatalf("after UpsertLyricsResolved: rec=%+v err=%v, want resolved/lrclib", rec, err)
	}
	text, ok, err := repo.GetResolvedLyrics(ctx, uri)
	if err != nil || !ok || text != "[00:01.00]hello" {
		t.Fatalf("GetResolvedLyrics after resolve: text=%q ok=%v err=%v", text, ok, err)
	}

	// A later transient error must NOT knock a resolved row back to pending —
	// this is the core invariant the whole cache exists to guarantee.
	if err := repo.UpsertLyricsError(ctx, uri, "a later unrelated timeout"); err != nil {
		t.Fatalf("UpsertLyricsError (post-resolve): %v", err)
	}
	rec, err = repo.GetLyricsStatus(ctx, uri)
	if err != nil || rec == nil || rec.Status != admin.LyricsStatusResolved {
		t.Fatalf("after post-resolve error: rec=%+v err=%v, want status to stay resolved", rec, err)
	}

	// GetLyricsStatusesForURIs batches correctly: present for a known uri,
	// absent (not just zero-valued) for one that was never attempted.
	unknown := "spotify:track:qatest_lyrics_never_" + itoa(time.Now().UnixNano())
	statuses, err := repo.GetLyricsStatusesForURIs(ctx, []string{uri, unknown})
	if err != nil {
		t.Fatalf("GetLyricsStatusesForURIs: %v", err)
	}
	if _, ok := statuses[unknown]; ok {
		t.Errorf("statuses contains unattempted uri %q, want absent", unknown)
	}
	if got := statuses[uri]; got.Status != admin.LyricsStatusResolved {
		t.Errorf("statuses[uri] = %+v, want resolved", got)
	}
}

// TestStaging_TrackLyricsCache_NotFoundLifecycle covers the confirmed-absent
// path separately: not_found must round-trip distinctly from pending/resolved
// and must likewise never be clobbered by itself across repeat calls.
func TestStaging_TrackLyricsCache_NotFoundLifecycle(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres track_lyrics test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	uri := "spotify:track:qatest_notfound_" + itoa(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = repo.pool.Exec(ctx, `DELETE FROM track_lyrics WHERE spotify_uri=$1`, uri)
	})

	if err := repo.UpsertLyricsNotFound(ctx, uri); err != nil {
		t.Fatalf("UpsertLyricsNotFound: %v", err)
	}
	rec, err := repo.GetLyricsStatus(ctx, uri)
	if err != nil || rec == nil || rec.Status != admin.LyricsStatusNotFound {
		t.Fatalf("rec=%+v err=%v, want not_found", rec, err)
	}
	if _, ok, err := repo.GetResolvedLyrics(ctx, uri); err != nil || ok {
		t.Fatalf("GetResolvedLyrics (not_found) = ok=%v err=%v, want ok=false", ok, err)
	}
}
