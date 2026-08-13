import { mkdir, writeFile } from 'node:fs/promises'

import { expect, test } from '@playwright/test'

test('Pixi renderer meets the Stage A correctness and resource gate', async ({
  page,
}) => {
  test.setTimeout(90_000)
  await page.goto('/')
  await page.waitForFunction(
    () => window.__EDITOR_SPIKE__ !== undefined,
    null,
    {
      timeout: 60_000,
    },
  )
  const result = await page.evaluate(() => window.__EDITOR_SPIKE__!)
  const timingGateEnforced =
    process.env.EDITOR_SPIKE_ENFORCE_DEVICE_TIMING === '1'
  const deviceProfile =
    process.env.EDITOR_DEVICE_PROFILE ??
    (timingGateEnforced ? '' : 'shared-ci-or-local-unclassified')
  const report = {
    ...result,
    run: {
      deviceProfile,
      timingGateEnforced,
      capturedAt: new Date().toISOString(),
    },
  }
  await mkdir('output/playwright', { recursive: true })
  await writeFile(
    'output/playwright/editor-renderer-spike-report.json',
    JSON.stringify(report, null, 2),
  )
  expect(result.error).toBeUndefined()
  expect(result.statsBeforeDestroy.nodes).toBe(50)
  expect(result.statsBeforeDestroy.textures).toBe(50)
  expect(result.statsBeforeDestroy.estimatedTextureBytes).toBeLessThanOrEqual(
    50 * 640 * 640 * 4,
  )
  expect(result.syncMs).toBeLessThan(2_500)
  expect(result.renderP95Ms).toBeLessThan(8)
  expect(result.longTasks).toBeGreaterThanOrEqual(0)
  if (timingGateEnforced) {
    expect(
      deviceProfile,
      'fixed-device runs require EDITOR_DEVICE_PROFILE',
    ).not.toBe('')
    expect(result.environment.gpuRenderer).not.toMatch(
      /swiftshader|llvmpipe|software rasterizer/i,
    )
    expect(result.frameIntervalP95Ms).toBeLessThan(22.3)
    expect(result.longTasks).toBe(0)
  }
  expect(result.pixelMeanAbsoluteError).toBeLessThan(1)
  expect(result.pixelMismatchRatio).toBeLessThan(0.01)
  expect(result.v2PixelMeanAbsoluteError).toBeLessThan(1)
  expect(result.v2PixelMismatchRatio).toBeLessThan(0.01)
  expect(result.v2MaskRemovalMeanAbsoluteError).toBeLessThan(1)
  expect(result.v2MaskRemovalMismatchRatio).toBeLessThan(0.01)
  expect(result.resolutionTransitionBytes).toEqual([
    640 * 640 * 4,
    2048 * 2048 * 4,
    640 * 640 * 4,
  ])
  expect(result.contextLossSupported).toBe(true)
  expect(result.contextLostObserved).toBe(true)
  expect(result.contextRestoredObserved).toBe(true)
  expect(result.statsAfterDestroy).toEqual({
    nodes: 0,
    textures: 0,
    estimatedTextureBytes: 0,
    activeTextureBytes: 0,
    textureBudgetBytes: 256 << 20,
    textureBudgetExceeded: false,
    contextLost: false,
  })
})

declare global {
  interface Window {
    __EDITOR_SPIKE__?: {
      ok: boolean
      environment: {
        userAgent: string
        platform: string
        logicalProcessors: number
        deviceMemoryGiB?: number
        devicePixelRatio: number
        viewport: { width: number; height: number }
        screen: { width: number; height: number }
        gpuVendor: string
        gpuRenderer: string
      }
      initMs: number
      syncMs: number
      renderP50Ms: number
      renderP95Ms: number
      frameIntervalP95Ms: number
      longTasks: number
      pixelMeanAbsoluteError: number
      pixelMismatchRatio: number
      v2PixelMeanAbsoluteError: number
      v2PixelMismatchRatio: number
      v2MaskRemovalMeanAbsoluteError: number
      v2MaskRemovalMismatchRatio: number
      v2ActualBounds?: {
        left: number
        top: number
        right: number
        bottom: number
      }
      v2ExpectedBounds?: {
        left: number
        top: number
        right: number
        bottom: number
      }
      resolutionTransitionBytes: number[]
      contextLossSupported: boolean
      contextLostObserved: boolean
      contextRestoredObserved: boolean
      statsBeforeDestroy: {
        nodes: number
        textures: number
        estimatedTextureBytes: number
        activeTextureBytes: number
        textureBudgetBytes: number
        textureBudgetExceeded: boolean
        contextLost: boolean
      }
      statsAfterDestroy: {
        nodes: number
        textures: number
        estimatedTextureBytes: number
        activeTextureBytes: number
        textureBudgetBytes: number
        textureBudgetExceeded: boolean
        contextLost: boolean
      }
      error?: string
    }
  }
}
