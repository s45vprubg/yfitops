import { useState } from "react";
import type { AdminApi } from "../../useAdminApi";

interface Props {
  api: AdminApi;
  boardId: string;
  onImported: () => void;
}

export default function ImportPlaylist({ api, boardId, onImported }: Props) {
  const [uri, setUri] = useState("");
  const [importing, setImporting] = useState(false);
  const [result, setResult] = useState<string | null>(null);

  const handleImport = async () => {
    if (!uri.trim()) return;
    setImporting(true);
    setResult(null);
    try {
      const res = await api.importPlaylist(boardId, uri.trim());
      setResult(`Imported ${res.imported} tracks (${res.skipped} duplicates skipped)`);
      setUri("");
      onImported();
    } catch (e) {
      setResult(`Error: ${e instanceof Error ? e.message : "unknown"}`);
    } finally {
      setImporting(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex gap-1">
        <input
          type="text"
          placeholder="Paste Spotify playlist link or URI..."
          value={uri}
          onChange={(e) => setUri(e.target.value)}
          className="min-w-0 flex-1 rounded border border-edge bg-panel px-2 py-1 text-sm text-slate-200 placeholder-slate-500 outline-none focus:border-accent"
        />
        <button
          onClick={handleImport}
          disabled={importing || !uri.trim()}
          className="shrink-0 rounded bg-accent/20 px-3 py-1 text-sm text-accent hover:bg-accent/30 disabled:opacity-50"
        >
          {importing ? "Importing..." : "Import"}
        </button>
      </div>
      {result && (
        <div className={`text-xs ${result.startsWith("Error") ? "text-red-400" : "text-green-400"}`}>
          {result}
        </div>
      )}
    </div>
  );
}
