import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In development the Go backend runs on :8080; proxy API + WebSocket calls so
// the app works same-origin without any CORS configuration.
const backend = process.env.BACKEND_URL ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': backend,
      '/healthz': backend,
      '/ws': { target: backend.replace(/^http/, 'ws'), ws: true },
    },
  },
  build: { target: 'es2020', sourcemap: true },
});
