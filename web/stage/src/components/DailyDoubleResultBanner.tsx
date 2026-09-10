// DailyDoubleResultBanner — a transient overlay showing the average rating +
// points once a Daily Double resolves. Sanitized (avgStars/points only, no
// track data) — safe to render alongside any view.

interface Props {
  avgStars: number;
  points: number;
}

export default function DailyDoubleResultBanner({ avgStars, points }: Props) {
  return (
    <div className="fixed inset-x-0 top-10 z-30 flex justify-center">
      <div className="flex items-center gap-4 rounded-xl border border-neon-amber/40 bg-panel/90 px-8 py-4 shadow-[0_0_30px_rgba(255,191,0,0.2)] animate-winnerPop">
        <span className="text-sm uppercase tracking-[0.4em] text-neon-amber/70">daily double</span>
        <span className="text-2xl font-extrabold text-neon-amber neon-text">{avgStars.toFixed(1)}★</span>
        <span className="text-2xl font-extrabold text-neon-green neon-text">+{points}</span>
      </div>
    </div>
  );
}
