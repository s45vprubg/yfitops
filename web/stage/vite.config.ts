import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

// The shared contracts live in ../shared and are imported via the @shared alias.
// fs.allow is widened so Vite's dev server can serve files outside the project root.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@shared": fileURLToPath(new URL("../shared", import.meta.url)),
    },
  },
  server: {
    port: 8778, // stage dev server (backend HTTP is 8777)
    host: true, // expose on LAN so phones/projector can reach it
    fs: {
      allow: [
        fileURLToPath(new URL(".", import.meta.url)),
        fileURLToPath(new URL("../shared", import.meta.url)),
      ],
    },
  },
});
