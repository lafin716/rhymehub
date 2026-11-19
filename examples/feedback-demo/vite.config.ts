import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";

const HOSTS = [".ts.net", "localhost"];
const PORT = 5174;

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  // Requests to /api go to the rhymehub core. Same-origin calls avoid CORS in the browser.
  const proxy = {
    "/api": {
      target: env.VITE_RHYMEHUB_ENDPOINT || "http://127.0.0.1:7780",
      changeOrigin: true,
    },
  };

  return {
    plugins: [react()],
    server: {
      host: "0.0.0.0",
      port: PORT,
      strictPort: true,
      allowedHosts: HOSTS,
      proxy,
    },
    preview: {
      host: "0.0.0.0",
      port: PORT,
      strictPort: true,
      allowedHosts: HOSTS,
      proxy,
    },
  };
});
