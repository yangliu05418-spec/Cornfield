import {
  Application,
  Container,
  Graphics,
  ImageSource,
  Matrix,
  Sprite,
  Texture,
} from 'pixi.js'

import { ReferenceCountedResourceCache } from '../resources/resource-cache'
import { planEditorSceneAssetVariants } from '../resources/variant-plan'
import { compileEditorRenderScene } from './scene-compiler'
import type {
  EditorRenderDocument,
  EditorSceneRasterNode,
} from './scene-compiler'
import type {
  EditorRenderAsset,
  EditorRenderer,
  EditorRendererOptions,
  EditorRendererStats,
  EditorViewport,
} from './types'

type SceneNode = {
  container: Container
  sprite: Sprite
  mask?: Graphics
  assetID: string
  variantURL: string
}

type TextureResource = {
  texture: Texture
  bitmap: ImageBitmap
}

export class PixiEditorRenderer implements EditorRenderer {
  #app?: Application
  #world = new Container()
  #content = new Container()
  #artboardMask = new Graphics()
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
  #syncTail: Promise<void> = Promise.resolve()
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
    this.#onContextChange?.(false)
    if (this.#latestDocument && this.#latestAssets) {
      void this.sync(this.#latestDocument, this.#latestAssets).catch(
        this.#onError,
      )
    } else this.render()
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
  ) {
    this.#assertReady()
    this.#latestDocument = document
    this.#latestAssets = assets
    const task = this.#syncTail.then(() => this.#syncScene(document, assets))
    this.#syncTail = task.catch(() => undefined)
    return task
  }

  async #syncScene(
    document: EditorRenderDocument,
    assets: ReadonlyMap<string, EditorRenderAsset>,
  ) {
    if (this.#destroyed) return
    const scene = compileEditorRenderScene(document)
    const plan = planEditorSceneAssetVariants(
      scene,
      assets,
      this.#viewport,
      this.#resolution,
      this.#textureBudgetBytes,
    )
    this.#textureBudgetExceeded = plan.budgetExceeded
    this.#artboardMask
      .clear()
      .rect(0, 0, scene.canvas.width, scene.canvas.height)
      .fill(0xffffff)
    const live = new Set(scene.nodes.map((object) => object.id))
    for (const [id, node] of this.#nodes) {
      if (live.has(id)) continue
      this.#removeNode(id, node)
    }
    await mapWithConcurrency(scene.nodes, 6, async (object) => {
      const asset = assets.get(object.assetID)
      const variant = plan.variants.get(object.id)
      if (asset && variant) await this.#syncObject(object, asset, variant)
      else {
        const node = this.#nodes.get(object.id)
        if (node) this.#removeNode(object.id, node)
      }
    })
    this.#syncAlphaMasks(scene.nodes)
    this.#textures.prune(this.#textureBudgetBytes)
    this.#applyViewport()
    this.render()
  }

  setViewport(viewport: EditorViewport) {
    const zoomChanged = viewport.zoom !== this.#viewport.zoom
    this.#viewport = viewport
    this.#applyViewport()
    if (zoomChanged) this.#scheduleResourceReconcile()
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
    this.#app.renderer.render(this.#app.stage)
  }

  stats(): EditorRendererStats {
    const resources = this.#textures.stats()
    return {
      nodes: this.#nodes.size,
      textures: resources.entries,
      estimatedTextureBytes: resources.bytes,
      activeTextureBytes: resources.activeBytes,
      textureBudgetBytes: this.#textureBudgetBytes,
      textureBudgetExceeded: this.#textureBudgetExceeded,
      contextLost: this.#contextLost,
    }
  }

  destroy() {
    this.#destroyed = true
    window.clearTimeout(this.#resourceTimer)
    for (const [id, node] of this.#nodes) this.#removeNode(id, node)
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
    this.#textureBudgetExceeded = false
  }

  async settleResources() {
    window.clearTimeout(this.#resourceTimer)
    if (!this.#latestDocument || !this.#latestAssets || this.#destroyed) return
    await this.sync(this.#latestDocument, this.#latestAssets)
  }

  async #syncObject(
    object: EditorSceneRasterNode,
    asset: EditorRenderAsset,
    variant: EditorRenderAsset['variants'][number],
  ) {
    let node = this.#nodes.get(object.id)
    if (
      !node ||
      node.assetID !== object.assetID ||
      node.variantURL !== variant.url
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
      const sprite = new Sprite(texture)
      sprite.width = asset.width
      sprite.height = asset.height
      container.addChild(sprite)
      this.#content.addChild(container)
      node = {
        container,
        sprite,
        assetID: object.assetID,
        variantURL: variant.url,
      }
      this.#nodes.set(object.id, node)
    }
    node.container.setFromMatrix(new Matrix(...object.transform))
    node.container.alpha = object.role === 'mask' ? 1 : object.opacity
    node.sprite.alpha = 1
    node.container.visible = object.visible
    node.container.zIndex = object.order
    this.#syncCrop(node, object, asset)
  }

  #syncCrop(
    node: SceneNode,
    object: EditorSceneRasterNode,
    asset: EditorRenderAsset,
  ) {
    node.mask?.destroy()
    node.mask = undefined
    node.sprite.mask = null
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
    node.sprite.mask = mask
    node.mask = mask
  }

  #syncAlphaMasks(objects: readonly EditorSceneRasterNode[]) {
    const objectsByID = new Map(objects.map((object) => [object.id, object]))
    for (const node of this.#nodes.values()) {
      node.container.mask = null
      node.sprite.renderable = true
    }
    for (const object of objects) {
      if (!object.maskNodeID) continue
      const target = this.#nodes.get(object.id)
      const maskObject = objectsByID.get(object.maskNodeID)
      if (!target || !maskObject) continue
      const mask = this.#nodes.get(object.maskNodeID)
      target.container.alpha *= maskObject.opacity
      target.container.visible &&= maskObject.visible && mask !== undefined
      if (!target.container.visible || !mask) continue
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
    node.container.removeFromParent()
    node.container.destroy({ children: true })
    this.#textures.release(node.variantURL)
  }

  #scheduleResourceReconcile() {
    window.clearTimeout(this.#resourceTimer)
    if (!this.#latestDocument || !this.#latestAssets || this.#destroyed) return
    this.#resourceTimer = window.setTimeout(() => {
      if (!this.#latestDocument || !this.#latestAssets || this.#destroyed)
        return
      void this.sync(this.#latestDocument, this.#latestAssets).catch(
        this.#onError,
      )
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
