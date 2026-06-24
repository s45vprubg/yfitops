/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Neon / hacker palette. High contrast on near-black.
        ink: "#04060a",
        panel: "#0a0f18",
        neon: {
          green: "#35ff94",
          cyan: "#22d3ee",
          magenta: "#ff2bd6",
          amber: "#ffb000",
        },
      },
      fontFamily: {
        mono: ["'JetBrains Mono'", "'Fira Code'", "ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
        display: ["'JetBrains Mono'", "ui-monospace", "monospace"],
      },
      keyframes: {
        flicker: {
          "0%, 100%": { opacity: "1" },
          "92%": { opacity: "1" },
          "94%": { opacity: "0.4" },
          "96%": { opacity: "1" },
        },
        scan: {
          "0%": { transform: "translateY(-100%)" },
          "100%": { transform: "translateY(100%)" },
        },
        winnerPop: {
          "0%": { transform: "scale(0.6)", opacity: "0" },
          "60%": { transform: "scale(1.15)", opacity: "1" },
          "100%": { transform: "scale(1)", opacity: "1" },
        },
        pulseGlow: {
          "0%, 100%": { textShadow: "0 0 16px rgba(53,255,148,0.7), 0 0 40px rgba(53,255,148,0.35)" },
          "50%": { textShadow: "0 0 28px rgba(53,255,148,0.95), 0 0 72px rgba(53,255,148,0.55)" },
        },
      },
      animation: {
        flicker: "flicker 6s infinite",
        scan: "scan 7s linear infinite",
        winnerPop: "winnerPop 600ms cubic-bezier(.2,1.2,.3,1) both",
        pulseGlow: "pulseGlow 2.2s ease-in-out infinite",
      },
    },
  },
  plugins: [],
};
