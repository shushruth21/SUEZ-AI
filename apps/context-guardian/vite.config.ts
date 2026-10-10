import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Tauri expects a fixed dev-server port and must not have the terminal cleared
// (it hides Rust compiler output). src-tauri is watched by Tauri, not Vite.
export default defineConfig({
  plugins: [react()],
  clearScreen: false,
  server: {
    host: "127.0.0.1",
    port: 1420,
    strictPort: true,
    watch: { ignored: ["**/src-tauri/**"] }
  },
  envPrefix: ["VITE_", "TAURI_ENV_"],
  build: {
    target: "safari15",
    outDir: "dist",
    emptyOutDir: true
  },
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node"
  }
});
