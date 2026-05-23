import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Relative base so the built assets work when embedded and served from '/'.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: { '/api': 'http://localhost:8080' },
  },
})
