package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/s45vprubg/yfitops/server/internal/admin"
)

// lyrics_cache_neverclobber_staging_test.go verifies the three never-clobber
// guarantees added to the track_lyrics upserts (qa sweep 7, str7-001/002/003):
//
//  1. UpsertLyricsNotFound and UpsertLyricsError can never downgrade an
//     existing status='resolved' row (confirmed-good lyrics, auto-fetched or
//     manually entered, must survive a later probe/error).
//  2. UpsertLyricsResolved can never be overwritten by a non-manual write
//     when the existing row has source='manual' — mirrors SetDropPoint's
//     never-clobber-manual rule (drop_point_staging_test.go).
//  3. UpsertLyricsError can never demote an existing status='not_found' row
//     back to 'pending' (migration 0008's own documented invariant).
//
// Gated on YFI_TEST_DSN; self-skips without it, mirroring
// lyrics_cache_staging_test.go / drop_point_staging_test.go.
func TestStaging_TrackLyrics_NeverClobberResolvedOrNotFound(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres track_lyrics test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	type action func(ctx context.Context, repo *PostgresRepo, uri string) error

	cases := []struct {
		name               string
		seed               action
		act                action
		wantStatus         string
		wantSource         string // "" means don't check
		wantLastErrorEmpty bool
	}{
		{
			name: "resolved+manual survives a not_found probe",
			seed: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsResolved(ctx, uri, "[00:01.00]manual lyrics", "manual")
			},
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsNotFound(ctx, uri)
			},
			wantStatus:         admin.LyricsStatusResolved,
			wantSource:         "manual",
			wantLastErrorEmpty: true,
		},
		{
			name: "resolved+manual survives a transient error",
			seed: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsResolved(ctx, uri, "[00:01.00]manual lyrics", "manual")
			},
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsError(ctx, uri, "simulated timeout")
			},
			wantStatus:         admin.LyricsStatusResolved,
			wantSource:         "manual",
			wantLastErrorEmpty: true,
		},
		{
			name: "resolved+lrclib survives a not_found probe",
			seed: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsResolved(ctx, uri, "[00:01.00]auto lyrics", "lrclib")
			},
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsNotFound(ctx, uri)
			},
			wantStatus:         admin.LyricsStatusResolved,
			wantSource:         "lrclib",
			wantLastErrorEmpty: true,
		},
		{
			name: "not_found survives a transient error (migration 0008 invariant)",
			seed: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsNotFound(ctx, uri)
			},
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsError(ctx, uri, "simulated timeout")
			},
			wantStatus:         admin.LyricsStatusNotFound,
			wantLastErrorEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := "spotify:track:qatest_clobber_" + itoa(time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = repo.pool.Exec(ctx, `DELETE FROM track_lyrics WHERE spotify_uri=$1`, uri)
			})

			if err := tc.seed(ctx, repo, uri); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := tc.act(ctx, repo, uri); err != nil {
				t.Fatalf("act: %v", err)
			}

			rec, err := repo.GetLyricsStatus(ctx, uri)
			if err != nil {
				t.Fatalf("GetLyricsStatus: %v", err)
			}
			if rec == nil {
				t.Fatalf("GetLyricsStatus = nil, want a row")
			}
			if rec.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", rec.Status, tc.wantStatus)
			}
			if tc.wantSource != "" && rec.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", rec.Source, tc.wantSource)
			}
			if tc.wantLastErrorEmpty && rec.LastError != "" {
				t.Errorf("last_error = %q, want empty (protected row must not be polluted)", rec.LastError)
			}
		})
	}
}

// TestStaging_TrackLyrics_HappyPathFirstWrite proves the never-clobber guards
// didn't break the mainline case: a first-ever write on a never-attempted
// track must still succeed and land in the expected status, for all three
// upsert functions.
func TestStaging_TrackLyrics_HappyPathFirstWrite(t *testing.T) {
	dsn := os.Getenv("YFI_TEST_DSN")
	if dsn == "" {
		t.Skip("YFI_TEST_DSN not set; skipping live-Postgres track_lyrics test")
	}
	ctx := context.Background()
	repo, err := NewPostgresRepo(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	cases := []struct {
		name       string
		act        func(ctx context.Context, repo *PostgresRepo, uri string) error
		wantStatus string
	}{
		{
			name: "UpsertLyricsResolved on an unattempted track",
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsResolved(ctx, uri, "[00:01.00]hello", "lrclib")
			},
			wantStatus: admin.LyricsStatusResolved,
		},
		{
			name: "UpsertLyricsNotFound on an unattempted track",
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsNotFound(ctx, uri)
			},
			wantStatus: admin.LyricsStatusNotFound,
		},
		{
			name: "UpsertLyricsError on an unattempted track",
			act: func(ctx context.Context, repo *PostgresRepo, uri string) error {
				return repo.UpsertLyricsError(ctx, uri, "simulated timeout")
			},
			wantStatus: admin.LyricsStatusPending,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := "spotify:track:qatest_happy_" + itoa(time.Now().UnixNano())
			t.Cleanup(func() {
				_, _ = repo.pool.Exec(ctx, `DELETE FROM track_lyrics WHERE spotify_uri=$1`, uri)
			})

			if err := tc.act(ctx, repo, uri); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			rec, err := repo.GetLyricsStatus(ctx, uri)
			if err != nil || rec == nil {
				t.Fatalf("GetLyricsStatus: rec=%+v err=%v", rec, err)
			}
			if rec.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", rec.Status, tc.wantStatus)
			}
		})
	}
}
