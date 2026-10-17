import { useState } from "react";
import type { AdminApi, SoloKaraokeTrack } from "../../useAdminApi";

interface Props {
  api: AdminApi;
  track: SoloKaraokeTrack;
  onClose: () => void;
  onSaved: () => void;
}

// SoloKaraokeLyricsModal — adapted from builder/LyricsEditorModal.tsx. Only
// difference from the original: the save call resolves by trackId alone
// (either bucket, no boardId) via api.setSoloKaraokeLyricsManual, since Solo
// Karaoke's Library/Playlist buckets aren't board-scoped.
const TIMESTAMP_RE = /\[\d+:\d{1,2}(?:[.:]\d{1,3})?\]/;

export default function SoloKaraokeLyricsModal({ api, track, onClose, onSaved }: Props) {
  const [text, setText] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const looksLikeLRC = TIMESTAMP_RE.test(text);

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    try {
      await api.setSoloKaraokeLyricsManual(track.id, text);
      onSaved();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to save lyrics");
    }
    setSaving(false);
  };

  return (
    <div
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/70 backdrop-blur-sm"
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div className="flex w-[36rem] max-w-[90vw] flex-col gap-3 rounded-lg border border-edge bg-panel2 p-5 shadow-xl">
        <div>
          <div className="text-sm font-bold uppercase tracking-[0.15em] text-accent">Enter lyrics manually</div>
          <div className="text-xs text-slate-400">{track.song} — {track.artist}</div>
        </div>
        <textarea
          autoFocus
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={"[00:12.40]First line\n[00:14.00]Second line\n…"}
          rows={10}
          className="w-full resize-none rounded border border-edge bg-panel px-3 py-2 font-mono text-xs text-slate-100 outline-none focus:border-accent"
        />
        {text && !looksLikeLRC && (
          <div className="text-[11px] text-amber-400">
            This doesn't look like LRC text (no [mm:ss.xx] timestamps found) — it'll likely be rejected.
          </div>
        )}
        {error && <div className="text-[11px] text-red-400">{error}</div>}
        <div className="flex justify-end gap-2">
          <button
            onClick={onClose}
            className="rounded border border-edge bg-panel px-3 py-1.5 text-xs font-semibold text-slate-300 hover:text-white"
          >
            Cancel
          </button>
          <button
            onClick={handleSave}
            disabled={saving || !text.trim()}
            className="rounded border border-accent/60 bg-accent/10 px-3 py-1.5 text-xs font-bold uppercase text-accent hover:bg-accent/20 disabled:opacity-40"
          >
            {saving ? "Saving…" : "Save"}
          </button>
        </div>
      </div>
    </div>
  );
}
