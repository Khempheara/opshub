import path from 'node:path';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

const apiTarget = process.env.OPSHUB_API_URL ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, 'src') },
  },
  server: {
    port: 5173,
    // Same-origin in dev as in prod (nginx), so session cookies need no CORS.
    proxy: {
      '/api': apiTarget,
      '/docs': apiTarget,
      '/healthz': apiTarget,
      '/readyz': apiTarget,
    },
  },
  build: {
    sourcemap: true,
  },
});
