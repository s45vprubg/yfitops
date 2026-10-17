import { useEffect, useRef, useState } from "react";
import type { GameState, ModeLockData, SoloKaraokeNowPlayingData, SoloKaraokeSetRepeatData } from "@shared/protocol";
import type { AdminActions, PlaybackPosition } from "../../useAdmin";
import { Scrubber } from "../GameControls";

interface Props {
  actions: AdminActions;
  nowPlaying?: SoloKaraokeNowPlayingData;
  playback?: PlaybackPosition;
  gameState?: GameState;
  modeLock?: ModeLockData["owner"];
  volumePct?: number;
}

function formatMs(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${(s % 60).toString().padStart(2, "0")}`;
}

const REPEAT_CYCLE: SoloKaraokeSetRepeatData["repeat"][] = ["", "all", "one"];
const REPEAT_LABEL: Record<SoloKaraokeSetRepeatData["repeat"], string> = {
  "": "Repeat: Off",
  all: "Repeat: All",
  one: "Repeat: One",
};

// Player — Solo Karaoke's transport (play/pause/resume/stop/skip/seek/
// volume/shuffle/repeat) + full-metadata now-playing display. Admin isn't
// §4A sanitization-gated, so unlike stage/mobile this shows the real
// artist/song/album art.
//
// "active" (an SK session is live) is `modeLock === "solo_karaoke"` —
// mirrors the server's own soloKaraoke.active flag exactly (modeLockOwner()
// returns "solo_karaoke" iff that flag is true, solo_karaoke.go:455-464),
// and unlike `nowPlaying.index !== -1` it's correctly re-broadcast on EVERY
// deactivate path (explicit Stop, queue exhaustion) via broadcastModeLock —
// soloKaraokeNowPlaying itself is only re-sent on the next *play*, so a
// stopped-but-not-yet-replayed session would otherwise still look "active"
// if index were used instead. Pause/Resume/Stop/Next/Prev/Seek/Shuffle/
// Repeat only check soloKaraoke.active server-side (not the trivia state),
// so they're enabled whenever a session is active, regardless of gameState.
// Play is the one exception: the server rejects it with
// {code:"modeLocked"} while trivia is active (state not LOBBY/GAME_OVER) —
// mirrored here by disabling Play in that case. A rejected action still
// surfaces through the existing admin.error -> state.notice mechanism
// (App.tsx), not a new one.
export default function Player({ actions, nowPlaying, playback, gameState, modeLock, volumePct }: Props) {
  const active = modeLock === "solo_karaoke";
  const triviaActive = !!gameState && gameState !== "LOBBY" && gameState !== "GAME_OVER";
  const playDisabled = triviaActive;

  // Pause vs Resume: mirror GameControls.tsx's own manuallyPaused pattern,
  // but Solo Karaoke's playback.paused (relayed verbatim from the stage's
  // own stage.playerState) is authoritative here, so just read it directly
  // rather than tracking local optimistic state.
  const paused = !!playback?.paused;

  const [localVolume, setLocalVolume] = useState(volumePct ?? 100);
  const volumeTouched = useRef(false);
  useEffect(() => {
    if (volumeTouched.current) return;
    setLocalVolume(volumePct ?? 100);
  }, [volumePct]);
  useEffect(() => {
    if (!volumeTouched.current) return;
    const id = setTimeout(() => actions.setVolume(localVolume), 120);
    return () => clearTimeout(id);
  }, [localVolume, actions]);

  const cycleRepeat = () => {
    const cur = nowPlaying?.repeat ?? "";
    const next = REPEAT_CYCLE[(REPEAT_CYCLE.indexOf(cur) + 1) % REPEAT_CYCLE.length];
    actions.soloKaraokeSetRepeat(next);
  };

  return (
    <div className="flex h-full flex-col gap-3 overflow-hidden border-l border-edge p-3">
      <h3 className="text-sm font-semibold text-slate-300">Player</h3>

      {/* Now playing */}
      <div className="flex items-center gap-3 rounded border border-edge bg-panel2 p-3">
        {nowPlaying?.albumArt ? (
          <img src={nowPlaying.albumArt} alt="" className="h-16 w-16 shrink-0 rounded" />
        ) : (
          <div className="flex h-16 w-16 shrink-0 items-center justify-center rounded bg-panel text-slate-600">
            ♪
          </div>
        )}
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-bold text-slate-100" title={nowPlaying?.song}>
            {active && nowPlaying ? nowPlaying.song : "Nothing loaded"}
          </div>
          <div className="truncate text-xs text-slate-400" title={nowPlaying?.artist}>
            {active && nowPlaying ? nowPlaying.artist : "Press Play to start the queue"}
          </div>
          {active && nowPlaying && (
            <div className="mt-1 text-[10px] text-slate-500">
              Track {nowPlaying.index + 1} of {nowPlaying.queueLen}
            </div>
          )}
        </div>
      </div>

      {/* Scrub bar — the same component/broadcast as Control Room's
          GameControls.tsx Scrubber, wired to the Solo Karaoke seek action. */}
      {active && playback && playback.durationMs > 0 && (
        <div className="flex items-center gap-2">
          <Scrubber playback={playback} onSeek={actions.soloKaraokeSeek} />
          <span className="font-mono text-[10px] text-slate-500">
            {formatMs(playback.positionMs)} / {formatMs(playback.durationMs)}
          </span>
        </div>
      )}

      {/* Transport */}
      <div className="flex flex-wrap items-center gap-2">
        <button
          onClick={actions.soloKaraokePrev}
          disabled={!active}
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-slate-300 hover:bg-panel3 disabled:pointer-events-none disabled:opacity-30"
        >
          ⏮ Prev
        </button>

        {!active ? (
          <button
            onClick={() => actions.soloKaraokePlay()}
            disabled={playDisabled}
            title={playDisabled ? "A trivia game is active — end it before starting Solo Karaoke" : undefined}
            className="rounded border border-green-600 bg-green-950/50 px-3 py-1.5 text-xs font-bold uppercase text-green-300 hover:bg-green-900/60 disabled:pointer-events-none disabled:opacity-30"
          >
            Play
          </button>
        ) : paused ? (
          <button
            onClick={actions.soloKaraokeResume}
            className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-emerald-300 hover:bg-emerald-950/40"
          >
            Resume
          </button>
        ) : (
          <button
            onClick={actions.soloKaraokePause}
            className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-amber-300 hover:bg-amber-950/40"
          >
            Pause
          </button>
        )}

        <button
          onClick={actions.soloKaraokeNext}
          disabled={!active}
          className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-slate-300 hover:bg-panel3 disabled:pointer-events-none disabled:opacity-30"
        >
          Next ⏭
        </button>

        <button
          onClick={actions.soloKaraokeStop}
          disabled={!active}
          className="rounded border border-red-800 bg-red-950/40 px-3 py-1.5 text-xs font-bold uppercase text-red-300 hover:bg-red-900/50 disabled:pointer-events-none disabled:opacity-30"
        >
          Stop
        </button>
      </div>

      {/* Shuffle / Repeat */}
      <div className="flex items-center gap-2">
        <button
          onClick={() => actions.soloKaraokeSetShuffle(!(nowPlaying?.shuffle ?? false))}
          disabled={!active}
          className={`rounded border px-2 py-1 text-xs font-semibold disabled:pointer-events-none disabled:opacity-30 ${
            nowPlaying?.shuffle
              ? "border-accent/60 bg-accent/10 text-accent"
              : "border-edge bg-panel text-slate-400 hover:text-slate-200"
          }`}
          title="Shuffle picks a fresh random track each advance (not a persisted order)"
        >
          Shuffle {nowPlaying?.shuffle ? "On" : "Off"}
        </button>
        <button
          onClick={cycleRepeat}
          disabled={!active}
          className="rounded border border-edge bg-panel px-2 py-1 text-xs font-semibold text-slate-400 hover:text-slate-200 disabled:pointer-events-none disabled:opacity-30"
        >
          {REPEAT_LABEL[nowPlaying?.repeat ?? ""]}
        </button>
      </div>

      {/* Volume — reuses the existing admin.setVolume action/control (one
          physical device shared by trivia and Solo Karaoke), not a new
          admin.soloKaraoke.setVolume type. */}
      <div className="mt-auto flex items-center gap-2 border-t border-edge pt-3">
        <span className="text-[10px] uppercase tracking-wide text-slate-500" title="Playback volume">
          🔊
        </span>
        <input
          type="range"
          min={0}
          max={100}
          value={localVolume}
          onChange={(e) => { volumeTouched.current = true; setLocalVolume(Number(e.target.value)); }}
          className="flex-1 accent-accent"
          aria-label="Playback volume"
        />
        <span className="tnum w-8 text-right text-[10px] text-slate-500">{localVolume}%</span>
      </div>
    </div>
  );
}
