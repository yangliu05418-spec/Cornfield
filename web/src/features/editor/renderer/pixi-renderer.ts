import 'pixi.js/unsafe-eval'

import {
  Application,
  ColorMatrixFilter,
  Container,
  CullerPlugin,
  DarkenBlend,
  extensions,
  Graphics,
  ImageSource,
  LightenBlend,
  Matrix,
  OverlayBlend,
  Rectangle,
  Sprite,
  Texture,
} from 'pixi.js'

import { ReferenceCountedResourceCache } from '../resources/resource-cache'
import { planEditorSceneAssetVariants } from '../resources/variant-plan'
import { LatestSyncCoordinator } from './sync-coordinator'
import { compileEditorRenderScene } from './scene-compiler'
import { isIdentityEditorColorMatrixV1 } from './color-effects'
import { PixiRasterMaskedContentSurface } from './raster-masked-content-surface'
import type {
  EditorRenderDocument,
  EditorSceneRasterNode,
} from './scene-compiler'
import type {
  EditorRenderAsset,
  EditorRasterMaskRenderResource,
  EditorRenderer,
  EditorRendererOptions,
  EditorRendererStats,
  EditorViewport,
} from './types'

type SceneNode = {
  container: Container
  visual: Container | Sprite
  sprite?: Sprite
  rasterSurface?: PixiRasterMaskedContentSurface
  mask?: Graphics
  assetID: string
  variantURL: string
  maskResourceID?: string
  maskVersion?: number
  maskGeneration?: number
  colorFilter?: ColorMatrixFilter
  artboardID?: string
}

type SceneArtboard = {
  container: Container
  content: Container
  mask: Graphics
  background: Graphics
}

type RenderResources = {
  assets: ReadonlyMap<string, EditorRenderAsset>
  rasterMasks: ReadonlyMap<string, EditorRasterMaskRenderResource>
}

type TextureResource = {
  texture: Texture
  bitmap: ImageBitmap
}

extensions.add(DarkenBlend, LightenBlend, OverlayBlend, CullerPlugin)

export class PixiEditorRenderer implements EditorRenderer {
  #app?: Application
  #world = new Container()
  #content = new Container()
  #artboardMask = new Graphics()
  #artboards = new Map<string, SceneArtboard>()
  #nodes = new Map<string, SceneNode>()
  #textures = new ReferenceCountedResourceCache<TextureResource>((resource) => {
    resource.texture.destroy(true)
    resource.bitmap.close()
  })
  #viewport: EditorViewport = { zoom: 100, panX: 0, panY: 0 }
  #resolution = 1
  #textureBudgetBytes = 256 << 20
  #textureBudgetExceeded = false
  #resolutionUpgradeDelayMs = 150
  #resourceTimer?: number
  #latestDocument?: EditorRenderDocument
  #latestAssets?: ReadonlyMap<string, EditorRenderAsset>
  #latestRasterMasks: ReadonlyMap<string, EditorRasterMaskRenderResource> =
    new Map()
  #syncs = new LatestSyncCoordinator(
    (document: EditorRenderDocument, resources: RenderResources) =>
      this.#syncScene(document, resources),
  )
  #contextRecoveries = 0
  #destroyed = false
  #contextLost = false
  #onContextChange?: (lost: boolean) => void
  #onError?: (error: unknown) => void
  #canvas?: HTMLCanvasElement
  #lostHandler = (event: Event) => {
    event.preventDefault()
    this.#contextLost = true
    this.#onContextChange?.(true)
  }
  #restoredHandler = () => {
    this.#contextLost = false
    if (this.#latestDocument && this.#latestAssets) {
      void this.sync(
        this.#latestDocument,
        this.#latestAssets,
        this.#latestRasterMasks,
      )
        .then(() => {
          this.#contextRecoveries += 1
          this.#onContextChange?.(false)
        })
        .catch(this.#onError)
    } else {
      this.render()
      this.#contextRecoveries += 1
      this.#onContextChange?.(false)
    }
  }

  async init(canvas: HTMLCanvasElement, options: EditorRendererOptions) {
    if (this.#destroyed) throw new Error('renderer is destroyed')
    if (this.#app) throw new Error('renderer is already initialized')
    this.#canvas = canvas
    this.#resolution = options.resolution ?? 1
    this.#textureBudgetBytes = options.textureBudgetBytes ?? 256 << 20
    this.#resolutionUpgradeDelayMs = options.resolutionUpgradeDelayMs ?? 150
    this.#onContextChange = options.onContextChange
    this.#onError = options.onError
    canvas.addEventListener('webglcontextlost', this.#lostHandler)
    canvas.addEventListener('webglcontextrestored', this.#restoredHandler)
    const app = new Application()
    await app.init({
      canvas,
      width: options.width,
      height: options.height,
      preference: 'webgl',
      antialias: false,
      autoStart: false,
      backgroundAlpha: 0,
      resolution: this.#resolution,
      preserveDrawingBuffer: options.preserveDrawingBuffer ?? false,
      gcActive: true,
      gcMaxUnusedTime: 10_000,
      gcFrequency: 2_000,
      culler: { updateTransform: true },
    })
    this.#world.sortableChildren = true
    this.#content.sortableChildren = true
    this.#content.mask = this.#artboardMask
    this.#world.addChild(this.#content, this.#artboardMask)
    app.stage.addChild(this.#world)
    this.#app = app
  }

  async sync(
    document: EditorRenderDocument,
    assets: ReadonlyMap<string, EditorRenderAsset>,
    rasterMasks: ReadonlyMap<
      string,
      EditorRasterMaskRenderResource
    > = new Map(),
  ) {
    this.#assertReady()
    this.#latestDocument = document
    this.#latestAssets = assets
    this.#latestRasterMasks = rasterMasks
    return this.#syncs.enqueue(document, { assets, rasterMasks })
  }

  async #syncScene(document: EditorRenderDocument, resources: RenderResources) {
    if (this.#destroyed) return
    const scene = compileEditorRenderScene(document)
    const { assets, rasterMasks } = resources
    const sceneNodes = this.#visibleSceneNodes(scene)
    const plan = planEditorSceneAssetVariants(
      { ...scene, nodes: sceneNodes },
      assets,
      this.#viewport,
      this.#resolution,
      this.#textureBudgetBytes,
    )
    this.#textureBudgetExceeded = plan.budgetExceeded
    const live = new Set(sceneNodes.map((object) => object.id))
    for (const [id, node] of this.#nodes) {
      if (live.has(id)) continue
      this.#removeNode(id, node)
    }
    if (scene.artboards) this.#syncArtboards(scene.artboards)
    else {
      this.#clearArtboards()
      this.#artboardMask
        .clear()
        .rect(0, 0, scene.canvas.width, scene.canvas.height)
        .fill(0xffffff)
    }
    await mapWithConcurrency(sceneNodes, 6, async (object) => {
      const asset = assets.get(object.assetID)
      const variant = plan.variants.get(object.id)
      const rasterMask = object.pixelMask
        ? rasterMasks.get(object.pixelMask.resource_id)
        : undefined
      if (asset && variant && (!object.pixelMask || rasterMask))
        await this.#syncObject(object, asset, variant, rasterMask)
      else {
        const node = this.#nodes.get(object.id)
        if (node) this.#removeNode(object.id, node)
      }
    })
    this.#syncAlphaMasks(sceneNodes)
    this.#textures.prune(this.#textureBudgetBytes)
    this.#applyViewport()
    this.render()
  }

  setViewport(viewport: EditorViewport) {
    const viewChanged =
      viewport.zoom !== this.#viewport.zoom ||
      viewport.panX !== this.#viewport.panX ||
      viewport.panY !== this.#viewport.panY
    this.#viewport = viewport
    this.#applyViewport()
    if (viewChanged) this.#scheduleResourceReconcile()
  }

  resize(width: number, height: number) {
    if (!this.#app || width < 1 || height < 1) return
    this.#app.renderer.resize(width, height)
    this.render()
  }

  #applyViewport() {
    const scale = this.#viewport.zoom / 100
    this.#world.position.set(this.#viewport.panX, this.#viewport.panY)
    this.#world.scale.set(scale)
  }

  render() {
    if (!this.#app || this.#contextLost) return
    this.#app.render()
  }

  stats(): EditorRendererStats {
    const resources = this.#textures.stats()
    const syncs = this.#syncs.stats()
    let visibleNodes = 0
    for (const node of this.#nodes.values()) {
      if (node.container.visible && !node.container.culled) visibleNodes += 1
    }
    return {
      nodes: this.#nodes.size,
      visibleNodes,
      textures: resources.entries,
      estimatedTextureBytes: resources.bytes,
      activeTextureBytes: resources.activeBytes,
      textureBudgetBytes: this.#textureBudgetBytes,
      textureBudgetExceeded: this.#textureBudgetExceeded,
      contextLost: this.#contextLost,
      contextRecoveries: this.#contextRecoveries,
      syncPasses: syncs.passes,
      coalescedSyncs: syncs.coalesced,
    }
  }

  destroy() {
    this.#destroyed = true
    window.clearTimeout(this.#resourceTimer)
    this.#syncs.close()
    for (const [id, node] of this.#nodes) this.#removeNode(id, node)
    this.#clearArtboards()
    this.#textures.clear()
    if (this.#canvas) {
      this.#canvas.removeEventListener('webglcontextlost', this.#lostHandler)
      this.#canvas.removeEventListener(
        'webglcontextrestored',
        this.#restoredHandler,
      )
    }
    this.#app?.destroy(false, { children: true })
    this.#app = undefined
    this.#canvas = undefined
    this.#latestDocument = undefined
    this.#latestAssets = undefined
    this.#latestRasterMasks = new Map()
    this.#textureBudgetExceeded = false
    this.#syncs = new LatestSyncCoordinator((document, resources) =>
      this.#syncScene(document, resources),
    )
    this.#contextRecoveries = 0
  }

  async settleResources() {
    window.clearTimeout(this.#resourceTimer)
    if (!this.#latestDocument || !this.#latestAssets || this.#destroyed) return
    await this.sync(
      this.#latestDocument,
      this.#latestAssets,
      this.#latestRasterMasks,
    )
  }

  async #syncObject(
    object: EditorSceneRasterNode,
    asset: EditorRenderAsset,
    variant: EditorRenderAsset['variants'][number],
    rasterMask?: EditorRasterMaskRenderResource,
  ) {
    let node = this.#nodes.get(object.id)
    const maskResourceID = rasterMask?.id
    if (
      !node ||
      node.assetID !== object.assetID ||
      node.variantURL !== variant.url ||
      node.maskResourceID !== maskResourceID
    ) {
      const resource = await this.#textures.retain(
        variant.url,
        variant.width * variant.height * 4,
        () => this.#loadTexture(variant.url),
      )
      if (this.#destroyed) {
        this.#textures.release(variant.url)
        return
      }
      if (node) this.#removeNode(object.id, node)
      const texture = resource.texture
      const container = new Container()
      container.cullable = true
      container.cullArea = new Rectangle(0, 0, asset.width, asset.height)
      let sprite: Sprite | undefined
      let rasterSurface: PixiRasterMaskedContentSurface | undefined
      let visual: Container | Sprite
      if (rasterMask) {
        rasterSurface = new PixiRasterMaskedContentSurface(
          texture,
          rasterMask.width,
          rasterMask.height,
          rasterMask.defaultAlpha,
        )
        rasterSurface.apply(rasterMask.tiles)
        visual = rasterSurface.container
        container.addChild(visual)
      } else {
        sprite = new Sprite(texture)
        sprite.width = asset.width
        sprite.height = asset.height
        visual = sprite
        container.addChild(sprite)
      }
      const target = object.artboardID
        ? this.#artboards.get(object.artboardID)?.content
        : this.#content
      target?.addChild(container)
      node = {
        container,
        visual,
        sprite,
        rasterSurface,
        assetID: object.assetID,
        variantURL: variant.url,
        maskResourceID,
        maskVersion: rasterMask?.version,
        maskGeneration: rasterMask?.generation,
        artboardID: object.artboardID,
      }
      this.#nodes.set(object.id, node)
    } else if (node.artboardID !== object.artboardID) {
      const target = object.artboardID
        ? this.#artboards.get(object.artboardID)?.content
        : this.#content
      target?.addChild(node.container)
      node.artboardID = object.artboardID
    } else if (
      rasterMask &&
      node.rasterSurface &&
      (node.maskVersion !== rasterMask.version ||
        node.maskGeneration !== rasterMask.generation)
    ) {
      if (rasterMask.changedTiles)
        node.rasterSurface.apply(rasterMask.changedTiles)
      else node.rasterSurface.replace(rasterMask.tiles)
      node.maskVersion = rasterMask.version
      node.maskGeneration = rasterMask.generation
    }
    node.container.setFromMatrix(new Matrix(...object.transform))
    node.container.alpha = object.role === 'mask' ? 1 : object.opacity
    node.container.blendMode =
      object.role === 'mask' ? 'normal' : object.blendMode
    if (node.sprite) node.sprite.alpha = 1
    node.container.visible = object.visible
    node.container.zIndex = object.order
    this.#syncCrop(node, object, asset)
    this.#syncShapeMask(node, object, asset)
    this.#syncEffects(node, object)
  }

  #syncEffects(node: SceneNode, object: EditorSceneRasterNode) {
    if (isIdentityEditorColorMatrixV1(object.colorMatrix)) {
      if (node.sprite) node.sprite.filters = null
      node.rasterSurface?.setColorMatrix()
      node.colorFilter?.destroy()
      node.colorFilter = undefined
      return
    }
    if (node.rasterSurface) {
      node.rasterSurface.setColorMatrix(object.colorMatrix)
      return
    }
    if (!node.sprite) return
    const filter = node.colorFilter ?? new ColorMatrixFilter()
    filter.matrix = object.colorMatrix
    node.colorFilter = filter
    node.sprite.filters = [filter]
  }

  #syncCrop(
    node: SceneNode,
    object: EditorSceneRasterNode,
    asset: EditorRenderAsset,
  ) {
    node.mask?.destroy()
    node.mask = undefined
    node.visual.mask = null
    if (!object.crop) return
    const mask = new Graphics()
      .rect(
        object.crop.x * asset.width,
        object.crop.y * asset.height,
        object.crop.width * asset.width,
        object.crop.height * asset.height,
      )
      .fill(0xffffff)
    node.container.addChild(mask)
    node.visual.mask = mask
    node.mask = mask
  }

  #syncShapeMask(
    node: SceneNode,
    object: EditorSceneRasterNode,
    asset: EditorRenderAsset,
  ) {
    if (!object.shapeMask) return
    node.mask?.destroy()
    const { x, y, width, height, inverted, type } = object.shapeMask
    const left = x * asset.width
    const top = y * asset.height
    const mask = new Graphics()
    if (inverted) mask.rect(0, 0, asset.width, asset.height).fill(0xffffff)
    if (type === 'ellipse')
      mask
        .ellipse(
          left + (width * asset.width) / 2,
          top + (height * asset.height) / 2,
          (width * asset.width) / 2,
          (height * asset.height) / 2,
        )
        .fill(0xffffff)
    else
      mask
        .rect(left, top, width * asset.width, height * asset.height)
        .fill(0xffffff)
    if (inverted) mask.cut()
    node.container.addChild(mask)
    node.visual.mask = mask
    node.mask = mask
  }

  #syncAlphaMasks(objects: readonly EditorSceneRasterNode[]) {
    const objectsByID = new Map(objects.map((object) => [object.id, object]))
    for (const node of this.#nodes.values()) {
      node.container.mask = null
      if (node.sprite) node.sprite.renderable = true
    }
    for (const object of objects) {
      if (!object.maskNodeID) continue
      const target = this.#nodes.get(object.id)
      const maskObject = objectsByID.get(object.maskNodeID)
      if (!target || !maskObject) continue
      const mask = this.#nodes.get(object.maskNodeID)
      target.container.alpha *= maskObject.opacity
      target.container.visible &&= maskObject.visible && mask !== undefined
      if (!target.container.visible || !mask?.sprite) continue
      target.container.setMask({ mask: mask.sprite, channel: 'alpha' })
    }
  }

  async #loadTexture(url: string) {
    const response = await fetch(url, {
      credentials: 'same-origin',
      cache: 'force-cache',
    })
    if (!response.ok)
      throw new Error(`texture request failed (${response.status})`)
    const bitmap = await createImageBitmap(await response.blob(), {
      imageOrientation: 'from-image',
      premultiplyAlpha: 'premultiply',
      colorSpaceConversion: 'default',
    })
    return {
      texture: new Texture({
        source: new ImageSource({
          resource: bitmap,
          autoGarbageCollect: false,
        }),
        label: url,
      }),
      bitmap,
    }
  }

  #removeNode(id: string, node: SceneNode) {
    this.#nodes.delete(id)
    node.colorFilter?.destroy()
    node.colorFilter = undefined
    node.rasterSurface?.destroy()
    node.rasterSurface = undefined
    node.container.removeFromParent()
    node.container.destroy({ children: true })
    this.#textures.release(node.variantURL)
  }

  #syncArtboards(
    definitions: NonNullable<
      ReturnType<typeof compileEditorRenderScene>['artboards']
    >,
  ) {
    this.#content.visible = false
    this.#artboardMask.visible = false
    const live = new Set(definitions.map((definition) => definition.id))
    for (const [id, artboard] of this.#artboards) {
      if (live.has(id)) continue
      artboard.container.removeFromParent()
      artboard.container.destroy({ children: true })
      this.#artboards.delete(id)
    }
    for (const definition of definitions) {
      let artboard = this.#artboards.get(definition.id)
      if (!artboard) {
        const container = new Container()
        const background = new Graphics()
        const content = new Container()
        const mask = new Graphics()
        content.sortableChildren = true
        content.mask = mask
        container.addChild(background, content, mask)
        this.#world.addChild(container)
        artboard = { container, content, mask, background }
        this.#artboards.set(definition.id, artboard)
      }
      artboard.container.position.set(definition.x, definition.y)
      artboard.container.zIndex = definition.order
      artboard.container.visible = definition.visible
      artboard.background
        .clear()
        .rect(0, 0, definition.width, definition.height)
        .fill(0xf7f7f8)
      artboard.mask
        .clear()
        .rect(0, 0, definition.width, definition.height)
        .fill(0xffffff)
    }
  }

  #clearArtboards() {
    for (const [id, node] of this.#nodes) {
      if (node.artboardID) this.#removeNode(id, node)
    }
    for (const artboard of this.#artboards.values()) {
      artboard.container.removeFromParent()
      artboard.container.destroy({ children: true })
    }
    this.#artboards.clear()
    this.#content.visible = true
    this.#artboardMask.visible = true
  }

  #visibleSceneNodes(
    scene: ReturnType<typeof compileEditorRenderScene>,
  ): EditorSceneRasterNode[] {
    if (!scene.artboards || !this.#app) return scene.nodes
    const scale = this.#viewport.zoom / 100
    const margin = 512
    const visible = new Set(
      scene.artboards
        .filter((artboard) => {
          const left = this.#viewport.panX + artboard.x * scale
          const top = this.#viewport.panY + artboard.y * scale
          const right = left + artboard.width * scale
          const bottom = top + artboard.height * scale
          return (
            right >= -margin &&
            bottom >= -margin &&
            left <= this.#app!.screen.width + margin &&
            top <= this.#app!.screen.height + margin
          )
        })
        .map((artboard) => artboard.id),
    )
    return scene.nodes.filter(
      (node) => !node.artboardID || visible.has(node.artboardID),
    )
  }

  #scheduleResourceReconcile() {
    window.clearTimeout(this.#resourceTimer)
    if (!this.#latestDocument || !this.#latestAssets || this.#destroyed) return
    this.#resourceTimer = window.setTimeout(() => {
      if (!this.#latestDocument || !this.#latestAssets || this.#destroyed)
        return
      void this.sync(
        this.#latestDocument,
        this.#latestAssets,
        this.#latestRasterMasks,
      ).catch(this.#onError)
    }, this.#resolutionUpgradeDelayMs)
  }

  #assertReady() {
    if (!this.#app) throw new Error('renderer is not initialized')
  }
}

async function mapWithConcurrency<T>(
  values: readonly T[],
  concurrency: number,
  visit: (value: T) => Promise<void>,
) {
  let next = 0
  const worker = async () => {
    while (next < values.length) {
      const index = next
      next += 1
      await visit(values[index])
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(concurrency, values.length) }, worker),
  )
}
