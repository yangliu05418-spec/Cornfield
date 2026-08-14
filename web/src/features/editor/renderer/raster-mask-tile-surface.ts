import { BufferImageSource, Container, Sprite, Texture } from 'pixi.js'

import { RASTER_MASK_TILE_SIZE } from '../tools/raster-mask/tile-mask'
import type { RasterMaskTileSnapshot } from '../tools/raster-mask/tile-mask'

type TileNode = {
  source: BufferImageSource
  texture: Texture<BufferImageSource>
  sprite: Sprite
  bytes: number
}

export class PixiRasterMaskTileSurface {
  readonly container = new Container()
  readonly #tiles = new Map<string, TileNode>()
  readonly #width: number
  readonly #height: number
  readonly #defaultAlpha: number
  #uploads = 0
  #destroyed = false

  constructor(width: number, height: number, defaultAlpha = 255) {
    if (
      !Number.isInteger(width) ||
      !Number.isInteger(height) ||
      width < 1 ||
      height < 1 ||
      width > 8192 ||
      height > 8192 ||
      width * height > 36_000_000
    )
      throw new RangeError('invalid raster mask surface dimensions')
    if (
      !Number.isInteger(defaultAlpha) ||
      defaultAlpha < 0 ||
      defaultAlpha > 255
    )
      throw new RangeError('invalid raster mask surface default alpha')
    this.#width = width
    this.#height = height
    this.#defaultAlpha = defaultAlpha
  }

  apply(snapshots: readonly RasterMaskTileSnapshot[]) {
    this.#assertAlive()
    const seen = new Set<string>()
    for (const snapshot of snapshots) {
      this.#validateSnapshot(snapshot)
      const key = tileKey(snapshot.tileX, snapshot.tileY)
      if (seen.has(key))
        throw new TypeError('duplicate raster mask tile update')
      seen.add(key)
      if (isDefaultTile(snapshot.alpha, this.#defaultAlpha)) {
        this.#remove(key)
        continue
      }
      const existing = this.#tiles.get(key)
      if (
        existing &&
        existing.source.pixelWidth === snapshot.width &&
        existing.source.pixelHeight === snapshot.height
      ) {
        existing.source.resource = snapshot.alpha
        existing.source.update()
        this.#uploads += 1
        continue
      }
      if (existing) this.#remove(key)
      const source = new BufferImageSource({
        resource: snapshot.alpha,
        width: snapshot.width,
        height: snapshot.height,
        format: 'r8unorm',
        alphaMode: 'no-premultiply-alpha',
        scaleMode: 'linear',
        autoGenerateMipmaps: false,
        autoGarbageCollect: false,
        label: `raster-mask:${key}`,
      })
      const texture = new Texture({ source, label: `raster-mask:${key}` })
      const sprite = new Sprite(texture)
      sprite.position.set(
        snapshot.tileX * RASTER_MASK_TILE_SIZE,
        snapshot.tileY * RASTER_MASK_TILE_SIZE,
      )
      sprite.width = snapshot.width
      sprite.height = snapshot.height
      this.container.addChild(sprite)
      this.#tiles.set(key, {
        source,
        texture,
        sprite,
        bytes: snapshot.alpha.byteLength,
      })
      this.#uploads += 1
    }
  }

  stats() {
    let bytes = 0
    for (const tile of this.#tiles.values()) bytes += tile.bytes
    return {
      tiles: this.#tiles.size,
      bytes,
      uploads: this.#uploads,
      destroyed: this.#destroyed,
    }
  }

  maskSprite(tileX: number, tileY: number) {
    this.#assertAlive()
    return this.#tiles.get(tileKey(tileX, tileY))?.sprite
  }

  destroy() {
    if (this.#destroyed) return
    for (const key of [...this.#tiles.keys()]) this.#remove(key)
    this.container.destroy({ children: true })
    this.#destroyed = true
  }

  #remove(key: string) {
    const tile = this.#tiles.get(key)
    if (!tile) return
    this.#tiles.delete(key)
    tile.sprite.removeFromParent()
    tile.sprite.destroy()
    tile.texture.destroy(true)
  }

  #validateSnapshot(snapshot: RasterMaskTileSnapshot) {
    const left = snapshot.tileX * RASTER_MASK_TILE_SIZE
    const top = snapshot.tileY * RASTER_MASK_TILE_SIZE
    const expectedWidth = Math.min(RASTER_MASK_TILE_SIZE, this.#width - left)
    const expectedHeight = Math.min(RASTER_MASK_TILE_SIZE, this.#height - top)
    if (
      !Number.isInteger(snapshot.tileX) ||
      !Number.isInteger(snapshot.tileY) ||
      snapshot.tileX < 0 ||
      snapshot.tileY < 0 ||
      expectedWidth < 1 ||
      expectedHeight < 1 ||
      snapshot.width !== expectedWidth ||
      snapshot.height !== expectedHeight ||
      snapshot.alpha.length !== expectedWidth * expectedHeight
    )
      throw new TypeError('invalid raster mask tile snapshot')
  }

  #assertAlive() {
    if (this.#destroyed) throw new Error('raster mask surface is destroyed')
  }
}

function tileKey(tileX: number, tileY: number) {
  return `${tileX}:${tileY}`
}

function isDefaultTile(alpha: Uint8Array, defaultAlpha: number) {
  for (const value of alpha) if (value !== defaultAlpha) return false
  return true
}
