/// <reference types="vitest/config" />
import { defineConfig, type ProxyOptions } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// The rhymehub core (`rhymehub serve`) listens on 127.0.0.1:7780.
const API_TARGET = 'http://127.0.0.1:7780';

// Tailscale MagicDNS names (*.ts.net) and localhost may reach the dev/preview servers.
const ALLOWED_HOSTS = ['.ts.net', 'localhost'];

// Minimal view of the http-proxy server that Vite hands to `configure`.
interface ProxyServerLike {
  on(
    event: 'proxyRes',
    listener: (proxyRes: { headers: Record<string, string | string[] | undefined> }, req: { url?: string }) => void,
  ): unknown;
}

// Keep Server-Sent Events flowing through the proxy: no buffering, no caching.
const sseFriendly = (server: ProxyServerLike) => {
  server.on('proxyRes', (proxyRes, req) => {
    if (req.url?.includes('/events/stream')) {
      proxyRes.headers['cache-control'] = 'no-cache, no-transform';
      proxyRes.headers['x-accel-buffering'] = 'no';
    }
  });
};

const proxy: Record<string, ProxyOptions> = {
  '/api': { target: API_TARGET, configure: (server) => sseFriendly(server as unknown as ProxyServerLike) },
  '/healthz': { target: API_TARGET },
};

export default defineConfig({
  base: '/',
  plugins: [react(), tailwindcss()],
  server: {
    host: '0.0.0.0',
    allowedHosts: ALLOWED_HOSTS,
    proxy,
  },
  preview: {
    host: '0.0.0.0',
    allowedHosts: ALLOWED_HOSTS,
    proxy,
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
});
