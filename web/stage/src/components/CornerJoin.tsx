// CornerJoin — the small QR + join URL anchored in the corner of EVERY view so
// late joiners can always scan in (§8A note).

import { useEffect, useState } from "react";
import QRCode from "qrcode";
import { JOIN_URL } from "../config";

export function useQrDataUrl(text: string, size: number) {
  const [url, setUrl] = useState<string>("");
  useEffect(() => {
    let alive = true;
    QRCode.toDataURL(text, {
      width: size,
      margin: 1,
      errorCorrectionLevel: "M",
      color: { dark: "#04060a", light: "#35ff94" },
    })
      .then((u) => {
        if (alive) setUrl(u);
      })
      .catch(() => {
        if (alive) setUrl("");
      });
    return () => {
      alive = false;
    };
  }, [text, size]);
  return url;
}

export default function CornerJoin() {
  const qr = useQrDataUrl(JOIN_URL, 160);
  return (
    <div className="fixed bottom-5 right-5 z-40 flex items-center gap-3 rounded-lg border border-neon-green/30 bg-panel/80 p-2 backdrop-blur">
      {qr ? (
        <img src={qr} alt="join QR" className="h-16 w-16 rounded" />
      ) : (
        <div className="h-16 w-16 animate-pulse rounded bg-neon-green/10" />
      )}
      <div className="pr-1 text-right leading-tight">
        <div className="text-[10px] uppercase tracking-widest text-neon-green/60">join</div>
        <div className="text-sm font-semibold text-neon-green">{JOIN_URL.replace(/^https?:\/\//, "")}</div>
      </div>
    </div>
  );
}
