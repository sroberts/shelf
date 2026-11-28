import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { pwa } from './build/pwa.js';

// Builds into internal/ui/dist, which the Go binary embeds. In dev, /v1,
// /opds and /mcp go to a running `shelf serve` (SHELF_URL); SHELF_AUTH, as
// user:password, is added to each proxied request so the browser never has to
// answer a Basic prompt for the proxy.
const target = process.env.SHELF_URL || 'http://127.0.0.1:65095';
const auth = process.env.SHELF_AUTH;

const proxied = {
  target,
  changeOrigin: true,
  configure(proxy) {
    if (!auth) return;
    proxy.on('proxyReq', (req) => {
      req.setHeader('authorization', `Basic ${Buffer.from(auth).toString('base64')}`);
    });
  },
};

export default defineConfig({
  plugins: [svelte(), pwa()],
  server: {
    proxy: { '/v1': proxied, '/opds': proxied, '/mcp': proxied },
  },
  build: {
    outDir: '../internal/ui/dist',
    emptyOutDir: true,
  },
});
