import { useState, useMemo } from "react";
import { Droppable } from "@hello-pangea/dnd";
import type { AdminApi, TrackData } from "../../useAdminApi";
import TrackCard from "./TrackCard";
import SpotifySearch from "./SpotifySearch";
import ImportPlaylist from "./ImportPlaylist";
import { useModal } from "../Modal";

type SortKey = "none" | "title" | "artist";

interface Props {
  api: AdminApi;
  boardId: string;
  tracks: TrackData[];
  // Every track on the board (placed + unplaced) — used only for the lyrics
  // status count/actions below. `tracks` alone (the library/holding list)
  // undercounts once a board is built out, since placed tracks leave it: a
  // "no tracks pending" board can still have a dozen stuck-pending tracks
  // sitting in cells that this panel would otherwise never see.
  allTracks: TrackData[];
  onRefresh: () => void;
  onDeleteTrack: (trackId: string) => void;
  onToggleOverride: (trackId: string, override: boolean) => void;
  // Per-track lyrics actions, lifted to BoardBuilderPage so the same requery
  // state and manual-lyrics modal are shared with BuilderGrid — a track's
  // lyrics tooling shouldn't disappear just because it got placed on a cell.
  requeryingId: string | null;
  onRequeryLyrics: (trackId: string) => void;
  onOpenManualLyrics: (track: TrackData) => void;
}

export default function HoldingArea({
  api, boardId, tracks, allTracks, onRefresh, onDeleteTrack, onToggleOverride,
  requeryingId, onRequeryLyrics, onOpenManualLyrics,
}: Props) {
  const [sortBy, setSortBy] = useState<SortKey>("none");
  const [rescanning, setRescanning] = useState(false);
  const [retryingPending, setRetryingPending] = useState(false);
  const [rescanMsg, setRescanMsg] = useState<string | null>(null);
  const [building, setBuilding] = useState(false);
  const [aiError, setAiError] = useState<string | null>(null);
  const { confirm } = useModal();

  const noLyrics = allTracks.filter(
    (t) => (t.lyricsStatus === "pending" || t.lyricsStatus === "not_found") && !t.lyricsOverride
  ).length;
  const pendingCount = allTracks.filter((t) => t.lyricsStatus === "pending").length;

  // Surfaces what the backend actually did — previously discarded entirely,
  // which is why a bulk check always looked identical whether it did real
  // work or nothing at all (no new/eligible tracks).
  const describeResult = (verb: string, res: { attempted: number; resolved: number; truncated: boolean }) => {
    if (res.attempted === 0) return "Nothing to check — already up to date.";
    const more = res.truncated ? " More remain — click again." : "";
    return `${verb} ${res.attempted}, resolved ${res.resolved}.${more}`;
  };

  const handleRescan = async () => {
    setRescanning(true);
    setRescanMsg(null);
    try {
      const res = await api.rescanLyrics(boardId);
      setRescanMsg(describeResult("Checked", res));
      onRefresh();
    } catch (e) {
      setRescanMsg(e instanceof Error ? e.message : "Lyrics check failed to run.");
    }
    setRescanning(false);
  };

  const handleRetryPending = async () => {
    setRetryingPending(true);
    setRescanMsg(null);
    try {
      const res = await api.requeryPendingLyrics(boardId);
      setRescanMsg(describeResult("Retried", res));
      onRefresh();
    } catch (e) {
      setRescanMsg(e instanceof Error ? e.message : "Retry failed to run.");
    }
    setRetryingPending(false);
  };

  const handleAiBuild = async () => {
    const ok = await confirm({
      title: "Build board with AI?",
      body: "The AI will group your tracks into ~6 themed categories and lay out the board. This REPLACES the current layout (tracks return to the library first).",
      confirmLabel: "Build",
    });
    if (!ok) return;
    setBuilding(true);
    setAiError(null);
    try {
      await api.aiBuild(boardId);
      onRefresh();
    } catch (e) {
      setAiError(e instanceof Error ? e.message : "AI build failed");
    }
    setBuilding(false);
  };

  const sorted = useMemo(() => {
    if (sortBy === "none") return tracks;
    const key = sortBy === "title" ? "song" : "artist";
    return [...tracks].sort((a, b) => (a[key] ?? "").localeCompare(b[key] ?? ""));
  }, [tracks, sortBy]);

  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden border-r border-edge p-3">
      <h3 className="text-sm font-semibold text-slate-300">Track Library</h3>

      <SpotifySearch api={api} boardId={boardId} onTrackAdded={onRefresh} />
      <ImportPlaylist api={api} boardId={boardId} onImported={onRefresh} />

      {/* AI board builder: auto-categorize + lay out the whole board. */}
      <button
        onClick={handleAiBuild}
        disabled={building || tracks.length === 0}
        className="rounded border border-accent/60 bg-accent/10 px-2 py-1.5 text-xs font-bold uppercase tracking-wide text-accent hover:bg-accent/20 disabled:opacity-40"
        title="Group tracks into themed categories and lay out the board with AI"
      >
        {building ? "Building…" : "✨ Build with AI"}
      </button>
      {aiError && <div className="text-[11px] text-red-400">{aiError}</div>}

      <div className="flex items-center justify-between rounded border border-edge bg-panel2 px-2 py-1 text-[11px]">
        <span className={noLyrics > 0 ? "text-amber-400" : "text-slate-500"}>
          {noLyrics > 0 ? `⚠ ${noLyrics} without lyrics` : "✓ all tracks have lyrics"}
        </span>
        <div className="flex gap-1">
          {pendingCount > 0 && (
            <button
              onClick={handleRetryPending}
              disabled={retryingPending || rescanning}
              className="rounded border border-edge px-1.5 py-0.5 font-semibold text-amber-300 hover:text-amber-200 disabled:opacity-40"
              title="Retry LRCLIB for every track that errored/rate-limited"
            >
              {retryingPending ? "retrying…" : `Retry pending (${pendingCount})`}
            </button>
          )}
          <button
            onClick={handleRescan}
            disabled={rescanning || retryingPending}
            className="rounded border border-edge px-1.5 py-0.5 font-semibold text-slate-300 hover:text-white disabled:opacity-40"
            title="Probe LRCLIB for every never-attempted track"
          >
            {rescanning ? "checking…" : "Check lyrics"}
          </button>
        </div>
      </div>
      {rescanMsg && <div className="text-[11px] text-slate-400">{rescanMsg}</div>}

      <div className="flex items-center justify-between">
        <span className="text-xs text-slate-500">
          {tracks.length} unplaced {tracks.length === 1 ? "track" : "tracks"}
        </span>
        <div className="flex gap-1">
          {(["none", "title", "artist"] as SortKey[]).map((key) => (
            <button
              key={key}
              onClick={() => setSortBy(key)}
              className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${
                sortBy === key
                  ? "bg-accent/20 text-accent"
                  : "text-slate-500 hover:text-slate-300"
              }`}
            >
              {key === "none" ? "Default" : key === "title" ? "Title" : "Artist"}
            </button>
          ))}
        </div>
      </div>

      <Droppable droppableId="holding">
        {(provided) => (
          <div
            ref={provided.innerRef}
            {...provided.droppableProps}
            className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto"
          >
            {sorted.map((t, i) => (
              <TrackCard
                key={t.id}
                track={t}
                index={i}
                onDelete={onDeleteTrack}
                onToggleOverride={onToggleOverride}
                onRequeryLyrics={onRequeryLyrics}
                onOpenManualLyrics={onOpenManualLyrics}
                requeryingLyrics={requeryingId === t.id}
              />
            ))}
            {provided.placeholder}
          </div>
        )}
      </Droppable>
    </div>
  );
}
