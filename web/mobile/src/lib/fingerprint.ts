// Lightweight device fingerprint for §3.2 resume: a stable random ID persisted
// in localStorage. This is intentionally NOT a real browser fingerprint — it's
// a soft, privacy-preserving session anchor so a player can drop and resume
// their score on the same device.

const FP_KEY = "yfitops.deviceFP";
const HANDLE_KEY = "yfitops.handle";

function randomID(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  // Fallback for ancient WebViews.
  return "fp-" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export function getDeviceFP(): string {
  let fp = localStorage.getItem(FP_KEY);
  if (!fp) {
    fp = randomID();
    localStorage.setItem(FP_KEY, fp);
  }
  return fp;
}

export function getSavedHandle(): string {
  return localStorage.getItem(HANDLE_KEY) ?? "";
}

export function saveHandle(handle: string): void {
  localStorage.setItem(HANDLE_KEY, handle);
}
