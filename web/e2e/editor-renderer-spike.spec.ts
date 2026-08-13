import { mkdir, writeFile } from 'node:fs/promises'

import { expect, test } from '@playwright/test'

test('Pixi renderer meets the Stage A correctness and resource gate', async ({
  page,
}) => {
  await page.goto('/')
  await page.waitForFunction(
    () => window.__EDITOR_SPIKE__ !== undefined,
    null,
    {
      timeout: 60_000,
    },
  )
  const result = await page.evaluate(() => window.__EDITOR_SPIKE__!)
  await mkdir('output/playwright', { recursive: true })
  await writeFile(
    'output/playwright/editor-renderer-spike-report.json',
    JSON.stringify(result, null, 2),
  )
  expect(result.error).toBeUndefined()
  expect(result.statsBeforeDestroy.nodes).toBe(50)
  expect(result.statsBeforeDestroy.textures).toBe(50)
  expect(result.statsBeforeDestroy.estimatedTextureBytes).toBeLessThanOrEqual(
    50 * 640 * 640 * 4,
  )
  expect(result.syncMs).toBeLessThan(2_500)
  expect(result.renderP95Ms).toBeLessThan(8)
  expect(result.longTasks).toBe(0)
  expect(result.pixelMeanAbsoluteError).toBeLessThan(1)
  expect(result.pixelMismatchRatio).toBeLessThan(0.01)
  expect(result.contextLossSupported).toBe(true)
  expect(result.contextLostObserved).toBe(true)
  expect(result.contextRestoredObserved).toBe(true)
  expect(result.statsAfterDestroy).toEqual({
    nodes: 0,
    textures: 0,
    estimatedTextureBytes: 0,
    contextLost: false,
  })
})

declare global {
  interface Window {
    __EDITOR_SPIKE__?: {
      ok: boolean
      initMs: number
      syncMs: number
      renderP50Ms: number
      renderP95Ms: number
      frameIntervalP95Ms: number
      longTasks: number
      pixelMeanAbsoluteError: number
      pixelMismatchRatio: number
      contextLossSupported: boolean
      contextLostObserved: boolean
      contextRestoredObserved: boolean
      statsBeforeDestroy: {
        nodes: number
        textures: number
        estimatedTextureBytes: number
        contextLost: boolean
      }
      statsAfterDestroy: {
        nodes: number
        textures: number
        estimatedTextureBytes: number
        contextLost: boolean
      }
      error?: string
    }
  }
}
