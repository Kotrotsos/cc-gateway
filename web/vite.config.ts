import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";

// In dev, the Vite server proxies /api to the Go gateway on :8088. The build
// emits to dist/, which the Go binary embeds.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "./src") },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: "http://localhost:8088",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
