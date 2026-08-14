import { describe, expect, it } from 'vitest'

import type { RasterMaskTileSnapshot } from '../tools/raster-mask/tile-mask'
import { PixiRasterMaskTileSurface } from './raster-mask-tile-surface'

describe('PixiRasterMaskTileSurface', () => {
  it('creates sparse tiles, updates sources in place, and releases default tiles', () => {
    const surface = new PixiRasterMaskTileSurface(300, 270)
    surface.apply([tile(0, 0, 256, 256, 0)])
    expect(surface.stats()).toEqual({
      tiles: 1,
      bytes: 256 * 256,
      uploads: 1,
      destroyed: false,
    })
    const sprite = surface.container.children[0]
    expect(surface.maskSprite(0, 0)).toBe(sprite)

    surface.apply([tile(0, 0, 256, 256, 127)])
    expect(surface.container.children[0]).toBe(sprite)
    expect(surface.stats().uploads).toBe(2)

    surface.apply([tile(0, 0, 256, 256, 255)])
    expect(surface.stats()).toMatchObject({ tiles: 0, bytes: 0, uploads: 2 })
    expect(surface.container.children).toHaveLength(0)
    expect(surface.maskSprite(0, 0)).toBeUndefined()
  })

  it('uses natural edge dimensions and rejects malformed or duplicate updates', () => {
    const surface = new PixiRasterMaskTileSurface(300, 270)
    surface.apply([tile(1, 1, 44, 14, 0)])
    expect(surface.stats().bytes).toBe(44 * 14)
    expect(() => surface.apply([tile(1, 1, 256, 256, 0)])).toThrow(
      'invalid raster mask tile snapshot',
    )
    const edge = tile(1, 1, 44, 14, 0)
    expect(() => surface.apply([edge, edge])).toThrow(
      'duplicate raster mask tile update',
    )
  })

  it('destroys texture resources idempotently', () => {
    const surface = new PixiRasterMaskTileSurface(512, 512)
    surface.apply([tile(0, 0, 256, 256, 0), tile(1, 1, 256, 256, 0)])
    surface.destroy()
    surface.destroy()
    expect(surface.stats()).toEqual({
      tiles: 0,
      bytes: 0,
      uploads: 2,
      destroyed: true,
    })
    expect(() => surface.apply([tile(0, 0, 256, 256, 0)])).toThrow(
      'surface is destroyed',
    )
  })
})

function tile(
  tileX: number,
  tileY: number,
  width: number,
  height: number,
  alpha: number,
): RasterMaskTileSnapshot {
  const pixels = new Uint8Array(width * height)
  pixels.fill(alpha)
  return { tileX, tileY, width, height, alpha: pixels }
}
