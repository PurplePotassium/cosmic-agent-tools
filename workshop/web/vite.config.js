import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Workshop SPA. `base: './'` keeps asset URLs relative so the built index.html
// works when the Go binary serves it at '/'. Dev proxy forwards API + SSE to the
// Go backend on :4455 so `npm run dev` behaves like the embedded build.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:4455',
        changeOrigin: true,
      },
      '/events': {
        target: 'http://127.0.0.1:4455',
        changeOrigin: true,
        ws: false,
      },
    },
  },
});
