import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: '../server/dist', emptyOutDir: true, sourcemap: false },
  server: { proxy: { '/api': 'http://localhost:8080' } },
});
