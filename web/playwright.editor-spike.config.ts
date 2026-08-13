import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  testMatch: 'editor-renderer-spike.spec.ts',
  workers: 1,
  retries: 0,
  reporter: 'line',
  use: {
    baseURL: 'http://127.0.0.1:4180',
    channel: process.env.EDITOR_DEVICE_CHANNEL || undefined,
    headless: process.env.EDITOR_DEVICE_HEADLESS !== '0',
  },
  webServer: {
    command: 'pnpm exec vite --config vite.editor-spike.config.ts',
    url: 'http://127.0.0.1:4180',
    reuseExistingServer: false,
    timeout: 120_000,
  },
})
