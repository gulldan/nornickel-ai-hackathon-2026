import { fileURLToPath, URL } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// REST goes through the dev-server proxy so the SPA and the API share an
// origin. The WebSocket can either connect straight to the gateway (local Vite
// dev) or use the current SPA origin (Docker nginx runtime, which proxies /api
// and /api/v1/ws to the backend gateway).
//
// Point RAG_API at your nginx gateway for local Vite dev:
//   RAG_API=http://localhost:8080 bun run dev
// Set RAG_PUBLIC_API="" at build time for same-origin WebSocket URLs.
const target = process.env.RAG_API ?? "http://localhost:8080";
const publicGateway = process.env.RAG_PUBLIC_API ?? target;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  define: {
    __GATEWAY__: JSON.stringify(publicGateway),
  },
  build: {
    rollupOptions: {
      output: {
        // Don't add transitive deps of the entry as eager static imports.
        hoistTransitiveImports: false,
        // IMPORTANT: only group vendors that are already part of the *eager*
        // app shell (framework + always-present UI). Grouping a vendor that is
        // used *only* by lazy routes (pdf.js, KaTeX, the markdown
        // pipeline) made rolldown hoist that named chunk onto the entry, so
        // ~1.3 MB loaded on every first page even where it isn't rendered — the
        // reason the Metrics page felt slow. Those heavy libs are intentionally
        // left unnamed so they stay inside their lazy route chunks and load
        // only when actually needed.
        manualChunks(id) {
          if (
            /\/node_modules\/(react|react-dom|react-router|react-router-dom|scheduler)\//.test(id)
          ) {
            return "react-vendor";
          }
          if (
            /\/node_modules\/(@radix-ui|@floating-ui|aria-hidden|react-remove-scroll|react-remove-scroll-bar|react-style-singleton|use-sidecar|use-callback-ref|detect-node-es)/.test(
              id,
            )
          ) {
            return "radix";
          }
          if (/\/node_modules\/(framer-motion|motion-dom|motion-utils)\//.test(id)) {
            return "motion";
          }
          if (id.includes("/node_modules/lucide-react/")) {
            return "icons";
          }
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": { target, changeOrigin: true },
    },
  },
});
