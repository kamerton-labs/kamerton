// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only
import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The production build lands in web/dist and is embedded into the Go binary
// (web/embed.go). In development, API calls are proxied to a locally running
// core (`make run`).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, './src') },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 4096,
  },
  server: {
    proxy: {
      '/api': { target: process.env.KAMERTON_API ?? 'http://localhost:8080', changeOrigin: false },
    },
  },
})
