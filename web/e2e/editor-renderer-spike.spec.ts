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
  expect(result.statsBeforeDestroy.nodes).toBe(500)
  expect(result.statsBeforeDestroy.visibleNodes).toBeLessThan(75)
  expect(result.statsBeforeDestroy.textures).toBe(50)
  expect(result.statsBeforeDestroy.estimatedTextureBytes).toBeLessThanOrEqual(
    50 * 640 * 640 * 4,
  )
  expect(result.syncMs).toBeLessThan(2_500)
  expect(result.burstSyncMs).toBeLessThan(2_500)
  expect(result.burstCoalescedSyncs).toBeGreaterThanOrEqual(18)
  expect(result.burstSyncPasses).toBeLessThanOrEqual(2)
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
  expect(result.rasterMaskWorker.createMs).toBeLessThan(1_000)
  expect(result.rasterMaskWorker.strokeMs).toBeLessThan(2_500)
  expect(result.rasterMaskWorker.previewTiles).toBeGreaterThan(1)
  expect(result.rasterMaskWorker.changedPixels).toBeGreaterThan(1_000)
  expect(result.rasterMaskWorker.retainedHistoryBytes).toBeLessThanOrEqual(
    64 << 20,
  )
  expect(result.rasterMaskWorker.undoTiles).toBeGreaterThan(1)
  expect(result.rasterMaskWorker.redoTiles).toBe(
    result.rasterMaskWorker.undoTiles,
  )
  expect(result.rasterMaskPixi).toMatchObject({
    largeSurfaceContentTiles: 576,
    offsetVisibleAlpha: 255,
    offsetHiddenAlpha: 0,
    initialLeftAlpha: 255,
    initialRightAlpha: 0,
    updatedLeftAlpha: 0,
    updatedRightAlpha: 255,
    defaultLeftAlpha: 255,
    defaultRightAlpha: 255,
    uploads: 3,
    tilesAfterDefault: 0,
    bytesAfterDefault: 0,
  })
  expect(result.rasterMaskPixi.largeSurfaceCreateMs).toBeLessThan(1_000)
  expect(result.rasterMaskRenderer).toEqual({
    initialLeftAlpha: 255,
    initialRightAlpha: 0,
    updatedLeftAlpha: 0,
    updatedRightAlpha: 255,
  })
  expect(result.contextLossSupported).toBe(true)
  expect(result.contextLostObserved).toBe(true)
  expect(result.contextRestoredObserved).toBe(true)
  expect(result.statsAfterRecovery.contextLost).toBe(false)
  expect(result.statsAfterRecovery.contextRecoveries).toBe(1)
  expect(result.statsAfterDestroy).toEqual({
    nodes: 0,
    visibleNodes: 0,
    textures: 0,
    estimatedTextureBytes: 0,
    activeTextureBytes: 0,
    textureBudgetBytes: 256 << 20,
    textureBudgetExceeded: false,
    contextLost: false,
    contextRecoveries: 0,
    syncPasses: 0,
    coalescedSyncs: 0,
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
      burstSyncMs: number
      burstSyncPasses: number
      burstCoalescedSyncs: number
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
      rasterMaskWorker: {
        createMs: number
        strokeMs: number
        previewTiles: number
        changedPixels: number
        retainedHistoryBytes: number
        undoTiles: number
        redoTiles: number
      }
      rasterMaskPixi: {
        largeSurfaceCreateMs: number
        largeSurfaceContentTiles: number
        offsetVisibleAlpha: number
        offsetHiddenAlpha: number
        initialLeftAlpha: number
        initialRightAlpha: number
        updatedLeftAlpha: number
        updatedRightAlpha: number
        defaultLeftAlpha: number
        defaultRightAlpha: number
        uploads: number
        tilesAfterDefault: number
        bytesAfterDefault: number
      }
      rasterMaskRenderer: {
        initialLeftAlpha: number
        initialRightAlpha: number
        updatedLeftAlpha: number
        updatedRightAlpha: number
      }
      contextLossSupported: boolean
      contextLostObserved: boolean
      contextRestoredObserved: boolean
      statsBeforeDestroy: {
        nodes: number
        visibleNodes: number
        textures: number
        estimatedTextureBytes: number
        activeTextureBytes: number
        textureBudgetBytes: number
        textureBudgetExceeded: boolean
        contextLost: boolean
        contextRecoveries: number
        syncPasses: number
        coalescedSyncs: number
      }
      statsAfterRecovery: {
        nodes: number
        visibleNodes: number
        textures: number
        estimatedTextureBytes: number
        activeTextureBytes: number
        textureBudgetBytes: number
        textureBudgetExceeded: boolean
        contextLost: boolean
        contextRecoveries: number
        syncPasses: number
        coalescedSyncs: number
      }
      statsAfterDestroy: {
        nodes: number
        visibleNodes: number
        textures: number
        estimatedTextureBytes: number
        activeTextureBytes: number
        textureBudgetBytes: number
        textureBudgetExceeded: boolean
        contextLost: boolean
        contextRecoveries: number
        syncPasses: number
        coalescedSyncs: number
      }
      error?: string
    }
  }
}
