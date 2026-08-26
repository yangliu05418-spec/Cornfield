import { defineConfig } from '@playwright/test'

const port = process.env.PLAYWRIGHT_PORT ?? '4173'
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${port}`

export default defineConfig({
  testDir: './e2e',
  testIgnore: 'editor-renderer-spike.spec.ts',
  outputDir: './output/playwright/test-results',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: [
    ['line'],
    ['html', { outputFolder: 'output/playwright/report', open: 'never' }],
  ],
  use: {
    baseURL,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
  webServer: {
    command: `pnpm preview --host 127.0.0.1 --port ${port}`,
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
