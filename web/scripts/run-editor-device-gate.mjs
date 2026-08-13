import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const profile = process.env.EDITOR_DEVICE_PROFILE?.trim()
if (!profile) {
  console.error(
    'Set EDITOR_DEVICE_PROFILE to a stable device label before running the fixed-device gate.',
  )
  process.exit(2)
}

const playwrightCLI = fileURLToPath(
  new URL('../node_modules/@playwright/test/cli.js', import.meta.url),
)
const result = spawnSync(
  process.execPath,
  [playwrightCLI, 'test', '--config', 'playwright.editor-spike.config.ts'],
  {
    cwd: process.cwd(),
    env: {
      ...process.env,
      EDITOR_DEVICE_PROFILE: profile,
      EDITOR_SPIKE_ENFORCE_DEVICE_TIMING: '1',
    },
    stdio: 'inherit',
  },
)

if (result.error) throw result.error
process.exit(result.status ?? 1)
