interface Props {
  title: string;
  sub: string;
}

// A generic Daily Double waiting placeholder — used for non-contestants during
// the offer/song-pick sub-phase ("X is deciding...") and for the contestant
// once their performance starts ("You're up — sing!"). No track metadata ever
// reaches this component (§4A) — just the two caller-supplied strings.
export function DailyDoubleWaitingScreen({ title, sub }: Props) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center animate-fadeIn">
      <div className="text-sm uppercase tracking-[0.3em] text-yellow-400">daily double</div>
      <div className="text-2xl font-semibold text-neutral-200">{title}</div>
      <div className="text-sm text-neutral-500">{sub}</div>
    </div>
  );
}
