import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    proxy: mode === "playground" ? undefined : {
      "/api": { target: "http://127.0.0.1:8080", changeOrigin: false },
    },
  },
}));
