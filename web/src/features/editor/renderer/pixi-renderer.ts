import {
  Application,
  Container,
  Graphics,
  ImageSource,
  Matrix,
  Sprite,
  Texture,
} from 'pixi.js'

import { selectEditorAssetVariant } from './types'
import type { EditorDocument, EditorObject } from '../domain/document'
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

type TextureRecord = {
  texture: Texture
  bitmap: ImageBitmap
  refs: number
  bytes: number
}

export class PixiEditorRenderer implements EditorRenderer {
  #app?: Application
  #world = new Container()
  #nodes = new Map<string, SceneNode>()
  #textures = new Map<string, TextureRecord>()
  #pendingTextures = new Map<string, Promise<TextureRecord>>()
  #viewport: EditorViewport = { zoom: 100, panX: 0, panY: 0 }
  #resolution = 1
  #contextLost = false
  #onContextChange?: (lost: boolean) => void
  #canvas?: HTMLCanvasElement
  #lostHandler = (event: Event) => {
    event.preventDefault()
    this.#contextLost = true
    this.#onContextChange?.(true)
  }
  #restoredHandler = () => {
    this.#contextLost = false
    this.#onContextChange?.(false)
    this.render()
  }

  async init(canvas: HTMLCanvasElement, options: EditorRendererOptions) {
    if (this.#app) throw new Error('renderer is already initialized')
    this.#canvas = canvas
    this.#resolution = options.resolution ?? 1
    this.#onContextChange = options.onContextChange
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
    app.stage.addChild(this.#world)
    this.#app = app
  }

  async sync(
    document: EditorDocument,
    assets: ReadonlyMap<string, EditorRenderAsset>,
  ) {
    this.#assertReady()
    const live = new Set(document.objects.map((object) => object.id))
    for (const [id, node] of this.#nodes) {
      if (live.has(id)) continue
      this.#removeNode(id, node)
    }
    await mapWithConcurrency(document.objects, 6, async (object) => {
      const asset = assets.get(object.asset_id)
      if (asset) await this.#syncObject(object, asset)
    })
    this.setViewport(this.#viewport)
    this.render()
  }

  setViewport(viewport: EditorViewport) {
    this.#viewport = viewport
    const scale = viewport.zoom / 100
    this.#world.position.set(viewport.panX, viewport.panY)
    this.#world.scale.set(scale)
  }

  render() {
    if (!this.#app || this.#contextLost) return
    this.#app.renderer.render(this.#app.stage)
  }

  stats(): EditorRendererStats {
    return {
      nodes: this.#nodes.size,
      textures: this.#textures.size,
      estimatedTextureBytes: [...this.#textures.values()].reduce(
        (total, record) => total + record.bytes,
        0,
      ),
      contextLost: this.#contextLost,
    }
  }

  destroy() {
    for (const [id, node] of this.#nodes) this.#removeNode(id, node)
    for (const record of this.#textures.values()) {
      record.texture.destroy(true)
      record.bitmap.close()
    }
    this.#textures.clear()
    this.#pendingTextures.clear()
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
  }

  async #syncObject(object: EditorObject, asset: EditorRenderAsset) {
    const requiredPixels = this.#requiredPixels(object, asset)
    const variant = selectEditorAssetVariant(asset, requiredPixels)
    if (!variant) return
    let node = this.#nodes.get(object.id)
    if (
      !node ||
      node.assetID !== object.asset_id ||
      node.variantURL !== variant.url
    ) {
      if (node) this.#removeNode(object.id, node)
      const texture = await this.#retainTexture(
        variant.url,
        variant.width,
        variant.height,
      )
      const container = new Container()
      const sprite = new Sprite(texture)
      sprite.width = asset.width
      sprite.height = asset.height
      container.addChild(sprite)
      this.#world.addChild(container)
      node = {
        container,
        sprite,
        assetID: object.asset_id,
        variantURL: variant.url,
      }
      this.#nodes.set(object.id, node)
    }
    node.container.setFromMatrix(new Matrix(...object.transform))
    node.container.alpha = object.opacity
    node.container.visible = object.visible
    node.container.zIndex = object.z_index
    this.#syncCrop(node, object, asset)
  }

  #syncCrop(node: SceneNode, object: EditorObject, asset: EditorRenderAsset) {
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

  #requiredPixels(object: EditorObject, asset: EditorRenderAsset) {
    const scaleX = Math.hypot(object.transform[0], object.transform[1])
    const scaleY = Math.hypot(object.transform[2], object.transform[3])
    const viewportScale = (this.#viewport.zoom / 100) * this.#resolution
    return Math.ceil(
      Math.max(asset.width * scaleX, asset.height * scaleY) * viewportScale,
    )
  }

  async #retainTexture(url: string, width: number, height: number) {
    const existing = this.#textures.get(url)
    if (existing) {
      existing.refs += 1
      return existing.texture
    }
    const pending = this.#pendingTextures.get(url)
    if (pending) {
      const record = await pending
      record.refs += 1
      return record.texture
    }
    const task = this.#loadTexture(url, width, height)
    this.#pendingTextures.set(url, task)
    try {
      const record = await task
      this.#textures.set(url, record)
      return record.texture
    } finally {
      this.#pendingTextures.delete(url)
    }
  }

  async #loadTexture(url: string, width: number, height: number) {
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
      refs: 1,
      bytes: width * height * 4,
    }
  }

  #removeNode(id: string, node: SceneNode) {
    this.#nodes.delete(id)
    node.container.removeFromParent()
    node.container.destroy({ children: true })
    const record = this.#textures.get(node.variantURL)
    if (!record) return
    record.refs -= 1
    if (record.refs > 0) return
    record.texture.destroy(true)
    record.bitmap.close()
    this.#textures.delete(node.variantURL)
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
