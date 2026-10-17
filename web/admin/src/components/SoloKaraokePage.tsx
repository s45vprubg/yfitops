import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { DragDropContext, type DropResult } from "@hello-pangea/dnd";
import type { GameState, ModeLockData, SoloKaraokeNowPlayingData } from "@shared/protocol";
import { createAdminApi, type SoloKaraokeTrack } from "../useAdminApi";
import type { AdminActions, PlaybackPosition } from "../useAdmin";
import LibraryBucket from "./soloKaraoke/LibraryBucket";
import PlaylistBucket from "./soloKaraoke/PlaylistBucket";
import Player from "./soloKaraoke/Player";
import SoloKaraokeLyricsModal from "./soloKaraoke/SoloKaraokeLyricsModal";

interface Props {
  secret: string;
  gameState?: GameState;
  modeLock?: ModeLockData["owner"];
  nowPlaying?: SoloKaraokeNowPlayingData;
  // Live scrubber feed — the SAME "playbackPosition" broadcast/state Control
  // Room's GameControls uses, reused unmodified (engine.go's
  // broadcastPlaybackPosition already branches internally on
  // soloKaraoke.active and emits the identical shape either way).
  playback?: PlaybackPosition;
  volumePct?: number;
  actions: AdminActions;
}

// SoloKaraokePage — modeled on BoardBuilderPage.tsx's DragDropContext +
// handleDragEnd (same non-atomic move+compensating-rollback shape, same
// movingIds double-fire guard), but standalone: no board selector, since the
// Library/Playlist buckets aren't board-scoped (0010_solo_karaoke.sql).
export default function SoloKaraokePage({ secret, gameState, modeLock, nowPlaying, playback, volumePct, actions }: Props) {
  const api = useMemo(() => createAdminApi(secret), [secret]);

  const [library, setLibrary] = useState<SoloKaraokeTrack[]>([]);
  const [playlist, setPlaylist] = useState<SoloKaraokeTrack[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [requeryingId, setRequeryingId] = useState<string | null>(null);
  const [requeryError, setRequeryError] = useState<string | null>(null);
  const [manualLyricsTrack, setManualLyricsTrack] = useState<SoloKaraokeTrack | null>(null);

  const errMsg = (e: unknown) => (e instanceof Error ? e.message : String(e));

  const refresh = useCallback(async () => {
    try {
      const [lib, pl] = await Promise.all([
        api.listSoloKaraokeLibrary(),
        api.listSoloKaraokePlaylist(),
      ]);
      setLibrary(lib ?? []);
      setPlaylist(pl ?? []);
    } catch {
      setLibrary([]);
      setPlaylist([]);
    }
  }, [api]);

  useEffect(() => { refresh(); }, [refresh]);

  const findTrack = (trackId: string): SoloKaraokeTrack | undefined =>
    library.find((t) => t.id === trackId) ?? playlist.find((t) => t.id === trackId);

  const handleDeleteFromLibrary = async (trackId: string) => {
    try {
      setError(null);
      await api.deleteSoloKaraokeLibraryTrack(trackId);
    } catch (e) {
      setError(`Delete failed: ${errMsg(e)}`);
    } finally {
      refresh();
    }
  };

  const handleDeleteFromPlaylist = async (trackId: string) => {
    try {
      setError(null);
      await api.deleteSoloKaraokePlaylistTrack(trackId);
    } catch (e) {
      setError(`Delete failed: ${errMsg(e)}`);
    } finally {
      refresh();
    }
  };

  // Lyrics tooling resolves against EITHER bucket server-side (trackId
  // alone), so one pair of handlers covers both buckets — mirrors
  // BoardBuilderPage's lifted requery/manual-lyrics state.
  const handleRequeryLyrics = async (trackId: string) => {
    setRequeryingId(trackId);
    setRequeryError(null);
    try {
      await api.requerySoloKaraokeLyrics(trackId);
      await refresh();
    } catch (e) {
      setRequeryError(errMsg(e));
    }
    setRequeryingId(null);
  };

  // Tracks ids currently mid-move — same rationale as BoardBuilderPage's
  // movingIds ref: @hello-pangea/dnd releases its drag session on mouseup
  // without waiting for this async handler, so a second drag on the same
  // still-rendered row before refresh() lands could double-execute a move.
  const movingIds = useRef(new Set<string>());

  const handleDragEnd = async (result: DropResult) => {
    if (!result.destination) return;

    const src = result.source;
    const dst = result.destination;
    const trackId = result.draggableId;

    if (movingIds.current.has(trackId)) return;
    movingIds.current.add(trackId);

    try {
      setError(null);

      if (src.droppableId === "skPlaylist" && dst.droppableId === "skPlaylist") {
        // Within-bucket reorder: the Playlist is ordered (position), unlike
        // Daily Double's unordered bucket, so a same-droppable drag-end is
        // meaningful here. Compute the new full ordered id list locally
        // (optimistic) and push it as a single atomic rewrite.
        if (dst.index === src.index) return;
        const reordered = [...playlist];
        const [moved] = reordered.splice(src.index, 1);
        reordered.splice(dst.index, 0, moved);
        setPlaylist(reordered);
        try {
          await api.reorderSoloKaraokePlaylist(reordered.map((t) => t.id));
        } catch (e) {
          setError(`Reorder failed: ${errMsg(e)}`);
          await refresh(); // revert the optimistic order
        }
      } else if (src.droppableId === "skLibrary" && dst.droppableId === "skPlaylist") {
        // MOVE: delete from the Library table, re-create in the Playlist
        // table (non-atomic, same compensate-on-failure shape as
        // BoardBuilderPage's Daily-Double branches).
        const track = findTrack(trackId);
        if (track) {
          const created = await api.addSoloKaraokePlaylistTrack({
            spotifyUri: track.spotifyUri,
            artist: track.artist,
            song: track.song,
            albumArt: track.albumArt,
            durationMs: track.durationMs,
          });
          try {
            await api.deleteSoloKaraokeLibraryTrack(trackId);
          } catch (deleteErr) {
            try {
              await api.deleteSoloKaraokePlaylistTrack(created.id);
              setError(`Move to Playlist failed, nothing changed: ${errMsg(deleteErr)}`);
            } catch (compensateErr) {
              setError(
                `Move to Playlist partially failed: "${track.song}" now exists in BOTH the Library and the Playlist (${errMsg(deleteErr)}; cleanup also failed: ${errMsg(compensateErr)}). Remove the duplicate from the Playlist using its own remove button.`,
              );
            }
          }
          // Reorder to the exact drop index — addSoloKaraokePlaylistTrack
          // always appends, so a drop anywhere but the end needs a follow-up
          // reorder call to land it where the admin actually dropped it.
          try {
            const after = await api.listSoloKaraokePlaylist();
            const withoutNew = after.filter((t) => t.id !== created.id);
            withoutNew.splice(dst.index, 0, created);
            await api.reorderSoloKaraokePlaylist(withoutNew.map((t) => t.id));
          } catch {
            // Non-fatal: the track landed in the Playlist (just at the end,
            // not the exact drop index) — the add itself already succeeded.
          }
        }
      } else if (src.droppableId === "skPlaylist" && dst.droppableId === "skLibrary") {
        // MOVE back out of the Playlist into the Library — reverse of the
        // branch above.
        const track = findTrack(trackId);
        if (track) {
          const created = await api.addSoloKaraokeLibraryTrack({
            spotifyUri: track.spotifyUri,
            artist: track.artist,
            song: track.song,
            albumArt: track.albumArt,
            durationMs: track.durationMs,
          });
          try {
            await api.deleteSoloKaraokePlaylistTrack(trackId);
          } catch (deleteErr) {
            try {
              await api.deleteSoloKaraokeLibraryTrack(created.id);
              setError(`Move out of Playlist failed, nothing changed: ${errMsg(deleteErr)}`);
            } catch (compensateErr) {
              setError(
                `Move out of Playlist partially failed: "${track.song}" now exists in BOTH the Library and the Playlist (${errMsg(deleteErr)}; cleanup also failed: ${errMsg(compensateErr)}). Remove the duplicate manually.`,
              );
            }
          }
        }
      }
      // skLibrary -> skLibrary: the Library is unordered (like Board
      // Builder's holding area) — no-op, same as that bucket.
    } catch (e) {
      setError(`Move failed: ${errMsg(e)}`);
    } finally {
      await refresh();
      movingIds.current.delete(trackId);
    }
  };

  return (
    <DragDropContext onDragEnd={handleDragEnd}>
      <div className="flex h-full flex-col bg-[#05070a]">
        {modeLock === "trivia" && (
          <div className="flex items-center gap-2 border-b border-amber-700/60 bg-amber-950/40 px-4 py-2 text-xs font-semibold text-amber-300">
            A trivia game is active — end it from Control Room before starting Solo Karaoke.
          </div>
        )}
        {error && (
          <div
            className="flex items-center gap-2 border-b border-red-900/60 bg-red-950/40 px-4 py-2 text-xs text-red-300"
            role="alert"
          >
            <span className="flex-1">{error}</span>
            <button
              onClick={() => setError(null)}
              className="rounded px-2 py-0.5 font-semibold text-red-400 hover:text-white"
            >
              Dismiss
            </button>
          </div>
        )}
        <div className="grid min-h-0 flex-1 grid-cols-[320px_1fr_320px]">
          <LibraryBucket
            api={api}
            tracks={library}
            onRefresh={refresh}
            onDeleteTrack={handleDeleteFromLibrary}
            requeryingId={requeryingId}
            onRequeryLyrics={handleRequeryLyrics}
            onOpenManualLyrics={setManualLyricsTrack}
          />
          <PlaylistBucket
            api={api}
            tracks={playlist}
            onRefresh={refresh}
            onDeleteTrack={handleDeleteFromPlaylist}
            requeryingId={requeryingId}
            onRequeryLyrics={handleRequeryLyrics}
            onOpenManualLyrics={setManualLyricsTrack}
            nowPlayingTrackId={nowPlaying?.trackId}
          />
          <Player
            actions={actions}
            nowPlaying={nowPlaying}
            playback={playback}
            gameState={gameState}
            modeLock={modeLock}
            volumePct={volumePct}
          />
        </div>
        {requeryError && (
          <div className="sticky bottom-0 z-10 border-t border-red-900/60 bg-red-950/95 px-4 py-2 text-xs text-red-300">
            {requeryError}
            <button onClick={() => setRequeryError(null)} className="ml-3 underline">Dismiss</button>
          </div>
        )}
        {manualLyricsTrack && (
          <SoloKaraokeLyricsModal
            api={api}
            track={manualLyricsTrack}
            onClose={() => setManualLyricsTrack(null)}
            onSaved={refresh}
          />
        )}
      </div>
    </DragDropContext>
  );
}
