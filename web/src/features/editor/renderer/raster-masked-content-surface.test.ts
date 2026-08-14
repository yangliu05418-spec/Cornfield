import { BufferImageSource, Texture } from 'pixi.js'
import { describe, expect, it, vi } from 'vitest'

import type { RasterMaskTileSnapshot } from '../tools/raster-mask/tile-mask'
import { PixiRasterMaskedContentSurface } from './raster-masked-content-surface'

vi.mock('pixi.js', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  return {
    ...actual,
    MaskFilter: class {
      destroy() {}
    },
  }
})

describe('PixiRasterMaskedContentSurface', () => {
  it('tiles shared content and only filters dirty mask tiles', () => {
    const texture = contentTexture(30, 27)
    const surface = new PixiRasterMaskedContentSurface(texture, 300, 270)
    expect(surface.stats()).toMatchObject({
      contentTiles: 4,
      maskedTiles: 0,
      tiles: 0,
    })
    expect(surface.container.children).toHaveLength(5)
    expect(surface.container.children[1]?.x).toBe(256)

    surface.apply([tile(1, 1, 44, 14, 0)])
    expect(surface.stats()).toMatchObject({
      contentTiles: 4,
      maskedTiles: 1,
      tiles: 1,
    })
    expect(surface.container.children[3]?.filters).toHaveLength(1)

    surface.apply([tile(1, 1, 44, 14, 255)])
    expect(surface.stats()).toMatchObject({
      contentTiles: 4,
      maskedTiles: 0,
      tiles: 0,
    })
    expect(surface.container.children[3]?.filters).toBeNull()
    surface.destroy()
    texture.destroy(true)
  })

  it('materializes only painted tiles for transparent defaults', () => {
    const texture = contentTexture(512, 512)
    const surface = new PixiRasterMaskedContentSurface(texture, 512, 512, 0)
    expect(surface.stats().contentTiles).toBe(0)
    expect(surface.container.children).toHaveLength(1)
    surface.apply([tile(1, 0, 256, 256, 127)])
    expect(surface.stats()).toMatchObject({
      contentTiles: 1,
      maskedTiles: 1,
      tiles: 1,
    })
    surface.apply([tile(1, 0, 256, 256, 0)])
    expect(surface.stats()).toMatchObject({
      contentTiles: 0,
      maskedTiles: 0,
      tiles: 0,
    })
    surface.destroy()
    texture.destroy(true)
  })
})

function contentTexture(width: number, height: number) {
  const pixels = new Uint8Array(width * height * 4)
  pixels.fill(255)
  return new Texture({
    source: new BufferImageSource({
      resource: pixels,
      width,
      height,
      format: 'rgba8unorm',
    }),
  })
}

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
