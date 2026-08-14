import { PixiEditorRenderer } from '../../src/features/editor/renderer/pixi-renderer'
import type { EditorDocument } from '../../src/features/editor/domain/document'
import type { EditorDocumentV2 } from '../../src/features/editor/domain/document-v2'
import type { EditorRenderAsset } from '../../src/features/editor/renderer/types'
import { compileEditorRenderScene } from '../../src/features/editor/renderer/scene-compiler'
import { createRasterMaskWorkerClient } from '../../src/features/editor/tools/raster-mask/worker-client'

declare global {
  interface Window {
    __EDITOR_SPIKE__?: SpikeResult
  }
}

type SpikeResult = {
  ok: boolean
  environment: SpikeEnvironment
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
  v2ActualBounds?: PixelBounds
  v2ExpectedBounds?: PixelBounds
  resolutionTransitionBytes: number[]
  rasterMaskWorker: RasterMaskWorkerSpike
  contextLossSupported: boolean
  contextLostObserved: boolean
  contextRestoredObserved: boolean
  statsBeforeDestroy: ReturnType<PixiEditorRenderer['stats']>
  statsAfterRecovery: ReturnType<PixiEditorRenderer['stats']>
  statsAfterDestroy: ReturnType<PixiEditorRenderer['stats']>
  error?: string
}

type SpikeEnvironment = {
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

type PixelBounds = { left: number; top: number; right: number; bottom: number }

type RasterMaskWorkerSpike = {
  createMs: number
  strokeMs: number
  previewTiles: number
  changedPixels: number
  retainedHistoryBytes: number
  undoTiles: number
  redoTiles: number
}

const output = document.querySelector('output')!
const canvas = document.querySelector<HTMLCanvasElement>('#performance')!
const correctnessCanvas =
  document.querySelector<HTMLCanvasElement>('#correctness')!
const v2CorrectnessCanvas =
  document.querySelector<HTMLCanvasElement>('#v2-correctness')!
void run().catch((error: unknown) => {
  const result: SpikeResult = {
    ok: false,
    environment: readEnvironment(canvas),
    initMs: 0,
    syncMs: 0,
    burstSyncMs: 0,
    burstSyncPasses: 0,
    burstCoalescedSyncs: 0,
    renderP50Ms: 0,
    renderP95Ms: 0,
    frameIntervalP95Ms: 0,
    longTasks: 0,
    pixelMeanAbsoluteError: Number.POSITIVE_INFINITY,
    pixelMismatchRatio: 1,
    v2PixelMeanAbsoluteError: Number.POSITIVE_INFINITY,
    v2PixelMismatchRatio: 1,
    v2MaskRemovalMeanAbsoluteError: Number.POSITIVE_INFINITY,
    v2MaskRemovalMismatchRatio: 1,
    resolutionTransitionBytes: [],
    rasterMaskWorker: emptyRasterMaskWorkerSpike(),
    contextLossSupported: false,
    contextLostObserved: false,
    contextRestoredObserved: false,
    statsBeforeDestroy: emptyStats(),
    statsAfterRecovery: emptyStats(),
    statsAfterDestroy: emptyStats(),
    error: error instanceof Error ? error.message : String(error),
  }
  window.__EDITOR_SPIKE__ = result
  output.value = JSON.stringify(result, null, 2)
})

async function run() {
  const rasterMaskWorker = await runRasterMaskWorkerFixture()
  const pixelComparison = await runPixelCorrectnessFixture(correctnessCanvas)
  const v2PixelComparison =
    await runV2PixelCorrectnessFixture(v2CorrectnessCanvas)
  const resolutionTransitionBytes = await runResolutionTransitionFixture()
  const assets = await buildAssets(50)
  const document = buildDocument(assets, 500)
  let contextLostObserved = false
  let contextRestoredObserved = false
  const renderer = new PixiEditorRenderer()
  const initStarted = performance.now()
  await renderer.init(canvas, {
    width: 1280,
    height: 720,
    resolution: 1,
    onContextChange: (lost) => {
      if (lost) contextLostObserved = true
      else contextRestoredObserved = true
    },
  })
  const initMs = performance.now() - initStarted
  renderer.setViewport({ zoom: 12, panX: 100, panY: 40 })
  const syncStarted = performance.now()
  await renderer.sync(document, assets)
  const syncMs = performance.now() - syncStarted
  const beforeBurst = renderer.stats()
  const burstStarted = performance.now()
  await Promise.all(
    Array.from({ length: 20 }, (_, index) =>
      renderer.sync(offsetFirstLayer(document, index + 1), assets),
    ),
  )
  const burstSyncMs = performance.now() - burstStarted
  const afterBurst = renderer.stats()
  const burstSyncPasses = afterBurst.syncPasses - beforeBurst.syncPasses
  const burstCoalescedSyncs =
    afterBurst.coalescedSyncs - beforeBurst.coalescedSyncs
  for (let index = 0; index < 30; index += 1) {
    await nextFrame()
    renderer.setViewport({
      zoom: 12,
      panX: 100 + index,
      panY: 40,
    })
    renderer.render()
  }
  const longTasks: PerformanceEntry[] = []
  const observer = new PerformanceObserver((list) =>
    longTasks.push(...list.getEntries()),
  )
  try {
    observer.observe({ type: 'longtask' })
  } catch {
    // Long Task API is not available in every browser; frame timing remains authoritative.
  }
  const renderTimes: number[] = []
  const frameIntervals: number[] = []
  let previousFrame = await nextFrame()
  for (let index = 0; index < 180; index += 1) {
    const frame = await nextFrame()
    frameIntervals.push(frame - previousFrame)
    previousFrame = frame
    const started = performance.now()
    renderer.setViewport({
      zoom: 12 + Math.sin(index / 24) * 2,
      panX: 100 + index * 1.5,
      panY: 40 + Math.sin(index / 15) * 20,
    })
    renderer.render()
    renderTimes.push(performance.now() - started)
  }
  observer.disconnect()
  const statsBeforeDestroy = renderer.stats()
  const gl = canvas.getContext('webgl2') ?? canvas.getContext('webgl')
  const environment = readEnvironment(canvas, gl)
  const loseContext = gl?.getExtension('WEBGL_lose_context')
  const contextLossSupported = Boolean(loseContext)
  if (loseContext) {
    loseContext.loseContext()
    await delay(100)
    loseContext.restoreContext()
    await waitFor(() => renderer.stats().contextRecoveries > 0, 2_000)
  }
  const statsAfterRecovery = renderer.stats()
  renderer.destroy()
  for (const asset of assets.values())
    for (const variant of asset.variants) URL.revokeObjectURL(variant.url)
  const result: SpikeResult = {
    ok: true,
    environment,
    initMs,
    syncMs,
    burstSyncMs,
    burstSyncPasses,
    burstCoalescedSyncs,
    renderP50Ms: percentile(renderTimes, 0.5),
    renderP95Ms: percentile(renderTimes, 0.95),
    frameIntervalP95Ms: percentile(frameIntervals, 0.95),
    longTasks: longTasks.length,
    pixelMeanAbsoluteError: pixelComparison.meanAbsoluteError,
    pixelMismatchRatio: pixelComparison.mismatchRatio,
    v2PixelMeanAbsoluteError: v2PixelComparison.meanAbsoluteError,
    v2PixelMismatchRatio: v2PixelComparison.mismatchRatio,
    v2MaskRemovalMeanAbsoluteError:
      v2PixelComparison.maskRemovalMeanAbsoluteError,
    v2MaskRemovalMismatchRatio: v2PixelComparison.maskRemovalMismatchRatio,
    v2ActualBounds: v2PixelComparison.actualBounds,
    v2ExpectedBounds: v2PixelComparison.expectedBounds,
    resolutionTransitionBytes,
    rasterMaskWorker,
    contextLossSupported,
    contextLostObserved,
    contextRestoredObserved,
    statsBeforeDestroy,
    statsAfterRecovery,
    statsAfterDestroy: renderer.stats(),
  }
  window.__EDITOR_SPIKE__ = result
  output.value = JSON.stringify(result, null, 2)
}

async function runRasterMaskWorkerFixture(): Promise<RasterMaskWorkerSpike> {
  const client = createRasterMaskWorkerClient()
  try {
    const createStarted = performance.now()
    await client.create(8_000, 4_500)
    const createMs = performance.now() - createStarted
    const strokeStarted = performance.now()
    const first = await client.beginStroke(
      'spike-stroke',
      {
        size: 96,
        hardness: 0.65,
        opacity: 0.8,
        spacing: 0.08,
        mode: 'erase',
        pressureSize: 0.7,
        pressureOpacity: 0.5,
      },
      { x: 120, y: 120, pressure: 0.25 },
    )
    const preview = await client.addPoints(
      'spike-stroke',
      Array.from({ length: 120 }, (_, index) => ({
        x: 120 + index * 24,
        y: 120 + Math.sin(index / 8) * 160,
        pressure: 0.25 + (index / 119) * 0.75,
      })),
    )
    const committed = await client.commitStroke('spike-stroke')
    const strokeMs = performance.now() - strokeStarted
    const undone = await client.undo()
    const redone = await client.redo()
    return {
      createMs,
      strokeMs,
      previewTiles: first.tiles.length + preview.tiles.length,
      changedPixels: committed.changedPixels,
      retainedHistoryBytes: committed.retainedHistoryBytes,
      undoTiles: undone.tiles.length,
      redoTiles: redone.tiles.length,
    }
  } finally {
    client.close()
  }
}

function emptyRasterMaskWorkerSpike(): RasterMaskWorkerSpike {
  return {
    createMs: 0,
    strokeMs: 0,
    previewTiles: 0,
    changedPixels: 0,
    retainedHistoryBytes: 0,
    undoTiles: 0,
    redoTiles: 0,
  }
}

async function runV2PixelCorrectnessFixture(targetCanvas: HTMLCanvasElement) {
  const sources = buildV2CorrectnessSources()
  const assets = new Map<string, EditorRenderAsset>()
  const urls: string[] = []
  for (const [id, source] of sources) {
    const url = URL.createObjectURL(await canvasToBlob(source))
    urls.push(url)
    assets.set(id, {
      id,
      width: source.width,
      height: source.height,
      variants: [{ url, width: source.width, height: source.height }],
    })
  }
  const fixture: EditorDocumentV2 = {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 256, height: 256 },
    nodes: [
      {
        id: 'mask',
        type: 'raster',
        parent_id: null,
        order_key: '00000001',
        transform: [1, 0, 0, 1, 76, 66],
        opacity: 0.65,
        blend_mode: 'normal',
        visible: true,
        locked: false,
        asset_id: 'mask-asset',
        effects: [],
      },
      {
        id: 'group',
        type: 'group',
        parent_id: null,
        order_key: '00000002',
        transform: [0, 1.2, -1.2, 0, 204, 54],
        opacity: 0.7,
        blend_mode: 'normal',
        visible: true,
        locked: false,
      },
      {
        id: 'masked-content',
        type: 'raster',
        parent_id: 'group',
        order_key: '00000001',
        transform: [1, 0, 0, 1, 0, 0],
        opacity: 0.8,
        blend_mode: 'normal',
        visible: true,
        locked: false,
        mask_id: 'mask',
        asset_id: 'content-asset',
        effects: [],
      },
    ],
  }
  const renderer = new PixiEditorRenderer()
  try {
    await renderer.init(targetCanvas, {
      width: 256,
      height: 256,
      resolution: 1,
      preserveDrawingBuffer: true,
    })
    renderer.setViewport({ zoom: 100, panX: 0, panY: 0 })
    await renderer.sync(fixture, assets)
    renderer.render()
    await nextFrame()
    const actual = await canvasPixels(targetCanvas)
    const expected = referenceV2Pixels(fixture, sources)
    const unmaskedFixture = structuredClone(fixture)
    unmaskedFixture.nodes = unmaskedFixture.nodes.filter(
      (node) => node.id !== 'mask',
    )
    const unmaskedContent = unmaskedFixture.nodes.find(
      (node) => node.id === 'masked-content',
    )!
    unmaskedContent.mask_id = undefined
    await renderer.sync(unmaskedFixture, assets)
    renderer.render()
    await nextFrame()
    const unmaskedActual = await canvasPixels(targetCanvas)
    const unmaskedExpected = referenceV2Pixels(unmaskedFixture, sources)
    const unmaskedComparison = comparePixels(
      unmaskedActual,
      unmaskedExpected,
      true,
    )
    return {
      ...comparePixels(actual, expected, true),
      maskRemovalMeanAbsoluteError: unmaskedComparison.meanAbsoluteError,
      maskRemovalMismatchRatio: unmaskedComparison.mismatchRatio,
      actualBounds: opaqueBounds(actual, fixture.canvas.width),
      expectedBounds: opaqueBounds(expected, fixture.canvas.width),
    }
  } finally {
    renderer.destroy()
    for (const url of urls) URL.revokeObjectURL(url)
  }
}

function buildV2CorrectnessSources() {
  const sources = new Map<string, HTMLCanvasElement>()
  const content = document.createElement('canvas')
  content.width = 112
  content.height = 84
  const contentContext = content.getContext('2d')!
  contentContext.fillStyle = '#d1fe17'
  contentContext.fillRect(0, 0, content.width, content.height)
  contentContext.fillStyle = '#7247c9'
  contentContext.fillRect(18, 12, 54, 42)
  sources.set('content-asset', content)

  const mask = document.createElement('canvas')
  mask.width = 120
  mask.height = 120
  const maskContext = mask.getContext('2d')!
  const gradient = maskContext.createRadialGradient(60, 60, 12, 60, 60, 54)
  gradient.addColorStop(0, 'rgba(255,255,255,1)')
  gradient.addColorStop(1, 'rgba(255,255,255,0)')
  maskContext.fillStyle = gradient
  maskContext.fillRect(0, 0, 120, 120)
  sources.set('mask-asset', mask)
  return sources
}

function referenceV2Pixels(
  fixture: EditorDocumentV2,
  sources: ReadonlyMap<string, HTMLCanvasElement>,
) {
  const reference = document.createElement('canvas')
  reference.width = fixture.canvas.width
  reference.height = fixture.canvas.height
  const context = reference.getContext('2d', { willReadFrequently: true })!
  const scene = compileEditorRenderScene(fixture)
  const masks = new Map(
    scene.nodes
      .filter((node) => node.role === 'mask')
      .map((node) => [node.id, node]),
  )
  for (const node of scene.nodes) {
    if (node.role !== 'content' || !node.visible || node.opacity === 0) continue
    const source = sources.get(node.assetID)
    if (!source) continue
    const layer = document.createElement('canvas')
    layer.width = fixture.canvas.width
    layer.height = fixture.canvas.height
    const layerContext = layer.getContext('2d')!
    layerContext.setTransform(...node.transform)
    layerContext.globalAlpha = node.opacity
    layerContext.drawImage(source, 0, 0)
    if (node.maskNodeID) {
      const mask = masks.get(node.maskNodeID)!
      const maskSource = sources.get(mask.assetID)!
      layerContext.resetTransform()
      layerContext.globalAlpha = mask.opacity
      layerContext.globalCompositeOperation = 'destination-in'
      layerContext.setTransform(...mask.transform)
      layerContext.drawImage(maskSource, 0, 0)
    }
    context.drawImage(layer, 0, 0)
  }
  return context.getImageData(0, 0, reference.width, reference.height).data
}

function readEnvironment(
  target: HTMLCanvasElement,
  context?: WebGLRenderingContext | WebGL2RenderingContext | null,
): SpikeEnvironment {
  const gl =
    context ?? target.getContext('webgl2') ?? target.getContext('webgl')
  const debug = gl?.getExtension('WEBGL_debug_renderer_info')
  const memory = (navigator as Navigator & { deviceMemory?: number })
    .deviceMemory
  return {
    userAgent: navigator.userAgent,
    platform: navigator.platform,
    logicalProcessors: navigator.hardwareConcurrency || 0,
    deviceMemoryGiB: memory,
    devicePixelRatio: window.devicePixelRatio || 1,
    viewport: { width: window.innerWidth, height: window.innerHeight },
    screen: { width: window.screen.width, height: window.screen.height },
    gpuVendor: gl
      ? String(
          gl.getParameter(debug?.UNMASKED_VENDOR_WEBGL ?? gl.VENDOR) ??
            'unknown',
        )
      : 'unavailable',
    gpuRenderer: gl
      ? String(
          gl.getParameter(debug?.UNMASKED_RENDERER_WEBGL ?? gl.RENDERER) ??
            'unknown',
        )
      : 'unavailable',
  }
}

async function runResolutionTransitionFixture() {
  const preview = document.createElement('canvas')
  preview.width = 640
  preview.height = 640
  preview.getContext('2d')!.fillRect(0, 0, 640, 640)
  const original = document.createElement('canvas')
  original.width = 2048
  original.height = 2048
  original.getContext('2d')!.fillRect(0, 0, 2048, 2048)
  const urls = [
    URL.createObjectURL(await canvasToBlob(preview)),
    URL.createObjectURL(await canvasToBlob(original)),
  ]
  const assets = new Map<string, EditorRenderAsset>([
    [
      'resolution',
      {
        id: 'resolution',
        width: 2048,
        height: 2048,
        variants: [
          { url: urls[0], width: 640, height: 640 },
          { url: urls[1], width: 2048, height: 2048 },
        ],
      },
    ],
  ])
  const fixture: EditorDocument = {
    schema_version: 1,
    canvas: { width: 2048, height: 2048 },
    objects: [
      {
        id: 'resolution-layer',
        asset_id: 'resolution',
        transform: [1, 0, 0, 1, 0, 0],
        opacity: 1,
        visible: true,
        locked: false,
        z_index: 0,
      },
    ],
  }
  const target = document.createElement('canvas')
  const renderer = new PixiEditorRenderer()
  try {
    await renderer.init(target, {
      width: 256,
      height: 256,
      resolution: 1,
      textureBudgetBytes: 20 << 20,
      resolutionUpgradeDelayMs: 1,
    })
    renderer.setViewport({ zoom: 10, panX: 0, panY: 0 })
    await renderer.sync(fixture, assets)
    const low = renderer.stats().activeTextureBytes
    renderer.setViewport({ zoom: 100, panX: 0, panY: 0 })
    await renderer.settleResources()
    const high = renderer.stats().activeTextureBytes
    renderer.setViewport({ zoom: 10, panX: 0, panY: 0 })
    await renderer.settleResources()
    const lowAgain = renderer.stats().activeTextureBytes
    return [low, high, lowAgain]
  } finally {
    renderer.destroy()
    for (const url of urls) URL.revokeObjectURL(url)
  }
}

async function runPixelCorrectnessFixture(targetCanvas: HTMLCanvasElement) {
  const sources = buildCorrectnessSources()
  const assets = new Map<string, EditorRenderAsset>()
  const urls: string[] = []
  for (const [id, source] of sources) {
    const blob = await canvasToBlob(source)
    const url = URL.createObjectURL(blob)
    urls.push(url)
    assets.set(id, {
      id,
      width: source.width,
      height: source.height,
      variants: [{ url, width: source.width, height: source.height }],
    })
  }
  const fixture: EditorDocument = {
    schema_version: 1,
    canvas: { width: 256, height: 256 },
    objects: [
      {
        id: 'cropped',
        name: 'Cropped base',
        asset_id: 'red-grid',
        transform: [1, 0, 0, 1, 20, 24],
        opacity: 1,
        visible: true,
        locked: false,
        z_index: 0,
        crop: { x: 0.125, y: 0.1, width: 0.75, height: 0.8 },
      },
      {
        id: 'rotated',
        name: 'Rotated overlay',
        asset_id: 'blue-grid',
        transform: [0, 1.5, -1.5, 0, 188, 72],
        opacity: 0.5,
        visible: true,
        locked: false,
        z_index: 1,
      },
      {
        id: 'flipped',
        name: 'Flipped foreground',
        asset_id: 'split-grid',
        transform: [-1.75, 0, 0, 1.75, 226, 164],
        opacity: 0.8,
        visible: true,
        locked: false,
        z_index: 2,
      },
    ],
  }
  const renderer = new PixiEditorRenderer()
  try {
    await renderer.init(targetCanvas, {
      width: 256,
      height: 256,
      resolution: 1,
      preserveDrawingBuffer: true,
    })
    renderer.setViewport({ zoom: 100, panX: 0, panY: 0 })
    await renderer.sync(fixture, assets)
    renderer.render()
    await nextFrame()
    const actual = await canvasPixels(targetCanvas)
    const expected = referencePixels(fixture, sources)
    return comparePixels(actual, expected)
  } finally {
    renderer.destroy()
    for (const url of urls) URL.revokeObjectURL(url)
  }
}

function buildCorrectnessSources() {
  const result = new Map<string, HTMLCanvasElement>()
  const red = document.createElement('canvas')
  red.width = 80
  red.height = 60
  const redContext = red.getContext('2d')!
  redContext.fillStyle = '#e45151'
  redContext.fillRect(0, 0, red.width, red.height)
  redContext.fillStyle = '#d1fe17'
  redContext.fillRect(0, 0, 24, red.height)
  result.set('red-grid', red)

  const blue = document.createElement('canvas')
  blue.width = 44
  blue.height = 28
  const blueContext = blue.getContext('2d')!
  blueContext.fillStyle = '#3377dd'
  blueContext.fillRect(0, 0, blue.width, blue.height)
  blueContext.fillStyle = '#f7f7f8'
  blueContext.fillRect(8, 6, 14, 10)
  result.set('blue-grid', blue)

  const split = document.createElement('canvas')
  split.width = 36
  split.height = 20
  const splitContext = split.getContext('2d')!
  splitContext.fillStyle = '#ff9c33'
  splitContext.fillRect(0, 0, 18, split.height)
  splitContext.fillStyle = '#7846d8'
  splitContext.fillRect(18, 0, 18, split.height)
  result.set('split-grid', split)
  return result
}

function referencePixels(
  fixture: EditorDocument,
  sources: ReadonlyMap<string, HTMLCanvasElement>,
) {
  const reference = document.createElement('canvas')
  reference.width = fixture.canvas.width
  reference.height = fixture.canvas.height
  const context = reference.getContext('2d', { willReadFrequently: true })!
  for (const object of [...fixture.objects].sort(
    (left, right) => left.z_index - right.z_index,
  )) {
    if (!object.visible || object.opacity === 0) continue
    const source = sources.get(object.asset_id)
    if (!source) continue
    context.save()
    context.setTransform(...object.transform)
    context.globalAlpha = object.opacity
    if (object.crop) {
      context.beginPath()
      context.rect(
        object.crop.x * source.width,
        object.crop.y * source.height,
        object.crop.width * source.width,
        object.crop.height * source.height,
      )
      context.clip()
    }
    context.drawImage(source, 0, 0, source.width, source.height)
    context.restore()
  }
  return context.getImageData(0, 0, reference.width, reference.height).data
}

async function canvasPixels(sourceCanvas: HTMLCanvasElement) {
  const bitmap = await createImageBitmap(await canvasToBlob(sourceCanvas))
  const copy = document.createElement('canvas')
  copy.width = sourceCanvas.width
  copy.height = sourceCanvas.height
  const context = copy.getContext('2d', { willReadFrequently: true })!
  context.drawImage(bitmap, 0, 0)
  bitmap.close()
  return context.getImageData(0, 0, copy.width, copy.height).data
}

function comparePixels(
  actual: Uint8ClampedArray,
  expected: Uint8ClampedArray,
  premultiplyColor = false,
) {
  if (actual.length !== expected.length) throw new Error('pixel buffers differ')
  let absoluteError = 0
  let mismatchedPixels = 0
  for (let index = 0; index < actual.length; index += 4) {
    let pixelMismatch = false
    for (let channel = 0; channel < 4; channel += 1) {
      const actualValue =
        premultiplyColor && channel < 3
          ? (actual[index + channel] * actual[index + 3]) / 255
          : actual[index + channel]
      const expectedValue =
        premultiplyColor && channel < 3
          ? (expected[index + channel] * expected[index + 3]) / 255
          : expected[index + channel]
      const difference = Math.abs(actualValue - expectedValue)
      absoluteError += difference
      if (difference > 16) pixelMismatch = true
    }
    if (pixelMismatch) mismatchedPixels += 1
  }
  return {
    meanAbsoluteError: absoluteError / actual.length,
    mismatchRatio: mismatchedPixels / (actual.length / 4),
  }
}

function opaqueBounds(pixels: Uint8ClampedArray, width: number) {
  let left = width
  let top = Math.ceil(pixels.length / 4 / width)
  let right = -1
  let bottom = -1
  for (let offset = 3; offset < pixels.length; offset += 4) {
    if (pixels[offset] === 0) continue
    const pixel = (offset - 3) / 4
    const x = pixel % width
    const y = Math.floor(pixel / width)
    left = Math.min(left, x)
    top = Math.min(top, y)
    right = Math.max(right, x)
    bottom = Math.max(bottom, y)
  }
  return right < 0 ? undefined : { left, top, right, bottom }
}

function canvasToBlob(sourceCanvas: HTMLCanvasElement) {
  return new Promise<Blob>((resolve, reject) =>
    sourceCanvas.toBlob(
      (value) =>
        value ? resolve(value) : reject(new Error('canvas encode failed')),
      'image/png',
    ),
  )
}

async function buildAssets(count: number) {
  const assets = new Map<string, EditorRenderAsset>()
  for (let index = 0; index < count; index += 1) {
    const preview = document.createElement('canvas')
    preview.width = 640
    preview.height = 640
    const context = preview.getContext('2d')!
    context.fillStyle = `hsl(${(index * 47) % 360} 45% 38%)`
    context.fillRect(0, 0, 640, 640)
    context.fillStyle = 'rgba(209,254,23,.75)'
    context.fillRect(80 + (index % 5) * 12, 80, 180, 440)
    const blob = await new Promise<Blob>((resolve, reject) =>
      preview.toBlob(
        (value) =>
          value ? resolve(value) : reject(new Error('texture encode failed')),
        'image/png',
      ),
    )
    const id = `asset-${index}`
    const previewURL = URL.createObjectURL(blob)
    const originalURL = URL.createObjectURL(blob)
    assets.set(id, {
      id,
      width: 4096,
      height: 4096,
      variants: [
        { url: previewURL, width: 640, height: 640 },
        { url: originalURL, width: 4096, height: 4096 },
      ],
    })
  }
  return assets
}

function buildDocument(
  assets: ReadonlyMap<string, EditorRenderAsset>,
  layerCount = assets.size,
): EditorDocument {
  const available = [...assets.values()]
  return {
    schema_version: 1,
    canvas: { width: 6000, height: 6000 },
    objects: Array.from({ length: layerCount }, (_, index) => {
      const asset = available[index % available.length]
      return {
        id: `layer-${index}`,
        name: `Layer ${index + 1}`,
        asset_id: asset.id,
        transform: [
          0.32,
          0,
          0,
          0.32,
          (index % 8) * 680 - 80,
          Math.floor(index / 8) * 760 - 120,
        ],
        opacity: 0.94,
        visible: true,
        locked: false,
        z_index: index,
        crop:
          index % 7 === 0
            ? { x: 0.08, y: 0.06, width: 0.84, height: 0.88 }
            : undefined,
      }
    }),
  }
}

function offsetFirstLayer(document: EditorDocument, offset: number) {
  const next = structuredClone(document)
  next.objects[0].transform[4] += offset
  return next
}

function percentile(values: number[], quantile: number) {
  const sorted = [...values].sort((a, b) => a - b)
  return (
    sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * quantile))] ??
    0
  )
}

function nextFrame() {
  return new Promise<number>((resolve) => requestAnimationFrame(resolve))
}

function delay(ms: number) {
  return new Promise<void>((resolve) => setTimeout(resolve, ms))
}

async function waitFor(predicate: () => boolean, timeoutMs: number) {
  const deadline = performance.now() + timeoutMs
  while (!predicate()) {
    if (performance.now() >= deadline) throw new Error('recovery timed out')
    await delay(25)
  }
}

function emptyStats() {
  return {
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
  }
}
