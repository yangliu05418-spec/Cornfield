import {
  ColorMatrixFilter,
  Container,
  MaskFilter,
  Rectangle,
  Sprite,
  Texture,
} from 'pixi.js'

import { RASTER_MASK_TILE_SIZE } from '../tools/raster-mask/tile-mask'
import type { RasterMaskTileSnapshot } from '../tools/raster-mask/tile-mask'
import { PixiRasterMaskTileSurface } from './raster-mask-tile-surface'

type ContentTile = {
  texture: Texture
  sprite: Sprite
  filter?: MaskFilter
  colorFilter?: ColorMatrixFilter
}

export class PixiRasterMaskedContentSurface {
  readonly container = new Container()
  readonly maskSurface: PixiRasterMaskTileSurface
  readonly #contentTexture: Texture
  readonly #contentWidth: number
  readonly #contentHeight: number
  readonly #defaultAlpha: number
  readonly #tiles = new Map<string, ContentTile>()
  readonly #maskedTiles = new Set<string>()
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
        this.#maskedTiles.delete(key)
        if (this.#defaultAlpha === 0) this.#removeTile(key)
        else this.#clearMask(this.#tiles.get(key))
        continue
      }
      this.#maskedTiles.add(key)
      const tile =
        this.#tiles.get(key) ?? this.#createTile(snapshot.tileX, snapshot.tileY)
      const maskSprite = this.maskSurface.maskSprite(
        snapshot.tileX,
        snapshot.tileY,
      )
      if (!maskSprite) throw new Error('raster mask tile was not materialized')
      if (!tile.filter) {
        tile.filter = new MaskFilter({ sprite: maskSprite, channel: 'red' })
        this.#syncFilters(tile)
      }
    }
  }

  replace(snapshots: readonly RasterMaskTileSnapshot[]) {
    this.#assertAlive()
    const next = new Set(
      snapshots.map((snapshot) => tileKey(snapshot.tileX, snapshot.tileY)),
    )
    const resets: RasterMaskTileSnapshot[] = []
    for (const key of this.#maskedTiles) {
      if (next.has(key)) continue
      const [tileX, tileY] = key.split(':').map(Number)
      const width = Math.min(
        RASTER_MASK_TILE_SIZE,
        this.#contentWidth - tileX * RASTER_MASK_TILE_SIZE,
      )
      const height = Math.min(
        RASTER_MASK_TILE_SIZE,
        this.#contentHeight - tileY * RASTER_MASK_TILE_SIZE,
      )
      const alpha = new Uint8Array(width * height)
      alpha.fill(this.#defaultAlpha)
      resets.push({ tileX, tileY, width, height, alpha })
    }
    this.apply([...resets, ...snapshots])
  }

  setColorMatrix(matrix?: readonly number[]) {
    this.#assertAlive()
    for (const tile of this.#tiles.values()) {
      if (!matrix) {
        tile.colorFilter?.destroy()
        delete tile.colorFilter
      } else {
        const filter = tile.colorFilter ?? new ColorMatrixFilter()
        filter.matrix = [...matrix] as typeof filter.matrix
        tile.colorFilter = filter
      }
      this.#syncFilters(tile)
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
    this.#maskedTiles.clear()
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
    tile.filter.destroy()
    delete tile.filter
    this.#syncFilters(tile)
  }

  #syncFilters(tile: ContentTile) {
    const filters = [tile.colorFilter, tile.filter].filter(
      (filter): filter is ColorMatrixFilter | MaskFilter =>
        filter !== undefined,
    )
    tile.sprite.filters = filters.length ? filters : null
  }

  #removeTile(key: string) {
    const tile = this.#tiles.get(key)
    if (!tile) return
    this.#tiles.delete(key)
    this.#clearMask(tile)
    tile.colorFilter?.destroy()
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
