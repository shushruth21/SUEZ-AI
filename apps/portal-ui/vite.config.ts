import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const edgeHost = process.env.EDGE_HOST ?? "suez.acrosystemsinc.ai";
const controlPlaneProxy =
  process.env.CONTROL_PLANE_PROXY_TARGET ?? "http://localhost:8081";
const gatewayProxy = process.env.GATEWAY_PROXY_TARGET ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    host: "0.0.0.0",
    port: 5173,
    allowedHosts: [edgeHost, "localhost", "127.0.0.1"],
    proxy: {
      "/control-plane": {
        target: controlPlaneProxy,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/control-plane/, "")
      },
      "/gateway": {
        target: gatewayProxy,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/gateway/, "")
      }
    }
  }
});
