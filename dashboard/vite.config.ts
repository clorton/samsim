import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": {
        target: process.env.AGGREGATOR_URL || "http://127.0.0.1:8080",
        ws: true,
      },
    },
  },
  preview: {
    port: 4173,
    proxy: {
      "/api": {
        target: process.env.AGGREGATOR_URL || "http://127.0.0.1:8080",
        ws: true,
      },
    },
  },
});
