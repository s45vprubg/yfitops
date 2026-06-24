/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: "#0a0a0f",
        panel: "#13131c",
        guess: "#35FF94",
        locked: "#4a1f24",
        danger: "#ff3b5c",
      },
      fontFamily: {
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
      keyframes: {
        pulseGlow: {
          "0%, 100%": { boxShadow: "0 0 0px 0px rgba(53,255,148,0.4)" },
          "50%": { boxShadow: "0 0 40px 8px rgba(53,255,148,0.6)" },
        },
        fadeIn: {
          "0%": { opacity: "0", transform: "scale(0.98)" },
          "100%": { opacity: "1", transform: "scale(1)" },
        },
      },
      animation: {
        pulseGlow: "pulseGlow 2.2s ease-in-out infinite",
        fadeIn: "fadeIn 0.25s ease-out",
      },
    },
  },
  plugins: [],
};
