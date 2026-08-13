import { defineConfig } from 'vite'

export default defineConfig({
  root: 'spikes/editor-renderer',
  build: {
    outDir: '../../output/editor-renderer-spike',
    emptyOutDir: true,
  },
  server: { host: '127.0.0.1', port: 4180 },
})
