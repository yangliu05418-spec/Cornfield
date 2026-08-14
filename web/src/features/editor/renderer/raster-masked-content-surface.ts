import { Container, MaskFilter, Rectangle, Sprite, Texture } from 'pixi.js'

import { RASTER_MASK_TILE_SIZE } from '../tools/raster-mask/tile-mask'
import type { RasterMaskTileSnapshot } from '../tools/raster-mask/tile-mask'
import { PixiRasterMaskTileSurface } from './raster-mask-tile-surface'

type ContentTile = {
  texture: Texture
  sprite: Sprite
  filter?: MaskFilter
}

export class PixiRasterMaskedContentSurface {
  readonly container = new Container()
  readonly maskSurface: PixiRasterMaskTileSurface
  readonly #contentTexture: Texture
  readonly #contentWidth: number
  readonly #contentHeight: number
  readonly #defaultAlpha: number
  readonly #tiles = new Map<string, ContentTile>()
  #destroyed = false

  constructor(
    contentTexture: Texture,
    contentWidth: number,
    contentHeight: number,
    defaultAlpha = 255,
  ) {
    this.maskSurface = new PixiRasterMaskTileSurface(
      contentWidth,
      contentHeight,
      defaultAlpha,
    )
    this.#contentTexture = contentTexture
    this.#contentWidth = contentWidth
    this.#contentHeight = contentHeight
    this.#defaultAlpha = defaultAlpha
    if (defaultAlpha === 255) this.#createAllTiles()
    this.maskSurface.container.renderable = false
    this.maskSurface.container.measurable = false
    this.maskSurface.container.eventMode = 'none'
    this.container.addChild(this.maskSurface.container)
  }

  apply(snapshots: readonly RasterMaskTileSnapshot[]) {
    this.#assertAlive()
    this.maskSurface.apply(snapshots)
    for (const snapshot of snapshots) {
      const key = tileKey(snapshot.tileX, snapshot.tileY)
      if (isDefaultTile(snapshot.alpha, this.#defaultAlpha)) {
        if (this.#defaultAlpha === 0) this.#removeTile(key)
        else this.#clearMask(this.#tiles.get(key))
        continue
      }
      const tile =
        this.#tiles.get(key) ?? this.#createTile(snapshot.tileX, snapshot.tileY)
      const maskSprite = this.maskSurface.maskSprite(
        snapshot.tileX,
        snapshot.tileY,
      )
      if (!maskSprite) throw new Error('raster mask tile was not materialized')
      if (!tile.filter) {
        tile.filter = new MaskFilter({ sprite: maskSprite, channel: 'red' })
        tile.sprite.filters = [tile.filter]
      }
    }
  }

  stats() {
    let maskedTiles = 0
    for (const tile of this.#tiles.values()) if (tile.filter) maskedTiles += 1
    return {
      contentTiles: this.#tiles.size,
      maskedTiles,
      ...this.maskSurface.stats(),
    }
  }

  destroy() {
    if (this.#destroyed) return
    for (const key of [...this.#tiles.keys()]) this.#removeTile(key)
    this.maskSurface.container.removeFromParent()
    this.maskSurface.destroy()
    this.container.destroy({ children: true })
    this.#destroyed = true
  }

  #createAllTiles() {
    const columns = Math.ceil(this.#contentWidth / RASTER_MASK_TILE_SIZE)
    const rows = Math.ceil(this.#contentHeight / RASTER_MASK_TILE_SIZE)
    for (let tileY = 0; tileY < rows; tileY += 1)
      for (let tileX = 0; tileX < columns; tileX += 1)
        this.#createTile(tileX, tileY)
  }

  #createTile(tileX: number, tileY: number) {
    const key = tileKey(tileX, tileY)
    const existing = this.#tiles.get(key)
    if (existing) return existing
    const left = tileX * RASTER_MASK_TILE_SIZE
    const top = tileY * RASTER_MASK_TILE_SIZE
    const width = Math.min(RASTER_MASK_TILE_SIZE, this.#contentWidth - left)
    const height = Math.min(RASTER_MASK_TILE_SIZE, this.#contentHeight - top)
    if (width < 1 || height < 1)
      throw new RangeError('invalid raster content tile coordinates')
    const sourceFrame = this.#contentTexture.frame
    const scaleX = sourceFrame.width / this.#contentWidth
    const scaleY = sourceFrame.height / this.#contentHeight
    const texture = new Texture({
      source: this.#contentTexture.source,
      frame: new Rectangle(
        sourceFrame.x + left * scaleX,
        sourceFrame.y + top * scaleY,
        width * scaleX,
        height * scaleY,
      ),
    })
    const sprite = new Sprite(texture)
    sprite.position.set(left, top)
    sprite.width = width
    sprite.height = height
    sprite.cullable = true
    const tile: ContentTile = { texture, sprite }
    this.#tiles.set(key, tile)
    this.container.addChild(sprite)
    return tile
  }

  #clearMask(tile: ContentTile | undefined) {
    if (!tile?.filter) return
    tile.sprite.filters = null
    tile.filter.destroy()
    delete tile.filter
  }

  #removeTile(key: string) {
    const tile = this.#tiles.get(key)
    if (!tile) return
    this.#tiles.delete(key)
    this.#clearMask(tile)
    tile.sprite.removeFromParent()
    tile.sprite.destroy()
    tile.texture.destroy(false)
  }

  #assertAlive() {
    if (this.#destroyed)
      throw new Error('raster masked content surface is destroyed')
  }
}

function tileKey(tileX: number, tileY: number) {
  return `${tileX}:${tileY}`
}

function isDefaultTile(alpha: Uint8Array, defaultAlpha: number) {
  for (const value of alpha) if (value !== defaultAlpha) return false
  return true
}
