import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Сборка идёт в контейнере: /out — это internal/web/dist (встраивается в бинарник).
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: process.env.OUT_DIR || 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
})
