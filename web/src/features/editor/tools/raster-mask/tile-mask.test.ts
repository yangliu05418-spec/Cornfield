import { describe, expect, it } from 'vitest'

import { RasterMaskHistory } from './history'
import type { RasterMaskBrush } from './tile-mask'
import { RASTER_MASK_TILE_SIZE, RasterMaskBuffer } from './tile-mask'

const hardEraser: RasterMaskBrush = {
  size: 20,
  hardness: 1,
  opacity: 1,
  spacing: 0.1,
  mode: 'erase',
  pressureSize: 0,
  pressureOpacity: 0,
}

describe('RasterMaskBuffer', () => {
  it('allocates only touched tiles and preserves edge tile dimensions', () => {
    const mask = new RasterMaskBuffer(600, 526)
    const stroke = mask.beginStroke(hardEraser, { x: 255, y: 260 })
    stroke.add({ x: 260, y: 260 })
    const patch = stroke.commit()

    expect(mask.allocatedTileCount).toBe(4)
    expect(patch.tiles.map((tile) => [tile.tileX, tile.tileY])).toEqual([
      [0, 0],
      [1, 0],
      [0, 1],
      [1, 1],
    ])
    expect(mask.readTile(2, 2)).toHaveLength((600 - 512) * (526 - 512))
    expect(mask.allocatedBytes).toBeLessThan(600 * 526)
  })

  it('samples long strokes without gaps across tile boundaries', () => {
    const mask = new RasterMaskBuffer(600, 64)
    const stroke = mask.beginStroke(
      { ...hardEraser, size: 12, spacing: 0.5 },
      { x: 4, y: 32 },
    )
    stroke.add({ x: 596, y: 32 })
    stroke.commit()

    for (let x = 4; x < 596; x++) expect(mask.readAlpha(x, 32)).toBe(0)
    expect(mask.allocatedTileCount).toBe(3)
  })

  it('uses hardness and pressure for deterministic falloff', () => {
    const mask = new RasterMaskBuffer(80, 80)
    const stroke = mask.beginStroke(
      {
        ...hardEraser,
        size: 40,
        hardness: 0,
        opacity: 0.8,
        pressureSize: 0.5,
        pressureOpacity: 1,
      },
      { x: 40, y: 40, pressure: 0.5 },
    )
    stroke.commit()

    const center = mask.readAlpha(40, 40)!
    const edge = mask.readAlpha(53, 40)!
    expect(center).toBeLessThan(edge)
    expect(center).toBeGreaterThan(0)
    expect(edge).toBeLessThan(255)
  })

  it('commits compact reversible regions and removes default tiles after undo', () => {
    const mask = new RasterMaskBuffer(512, 512)
    const patch = mask.beginStroke(hardEraser, { x: 100, y: 100 }).commit()
    const painted = mask.readAlpha(100, 100)

    expect(patch.tiles).toHaveLength(1)
    expect(patch.tiles[0].width).toBeLessThan(RASTER_MASK_TILE_SIZE)
    expect(patch.byteSize).toBe(patch.tiles[0].before.length * 2)
    mask.applyPatch(patch, 'backward')
    expect(mask.readAlpha(100, 100)).toBe(255)
    expect(mask.allocatedTileCount).toBe(0)
    mask.applyPatch(patch, 'forward')
    expect(mask.readAlpha(100, 100)).toBe(painted)
  })

  it('cancels a gesture without retaining pixels or a history patch', () => {
    const mask = new RasterMaskBuffer(512, 512)
    const stroke = mask.beginStroke(hardEraser, { x: 100, y: 100 })
    stroke.add({ x: 400, y: 400 })
    const patch = stroke.cancel()

    expect(patch).toEqual({ tiles: [], changedPixels: 0, byteSize: 0 })
    expect(mask.allocatedTileCount).toBe(0)
  })

  it('hydrates sparse immutable tile snapshots without allocating defaults', () => {
    const mask = new RasterMaskBuffer(300, 300)
    const alpha = new Uint8Array(256 * 256)
    alpha.fill(255)
    alpha[10 * 256 + 12] = 0
    mask.replaceTiles([{ tileX: 0, tileY: 0, width: 256, height: 256, alpha }])
    expect(mask.readAlpha(12, 10)).toBe(0)
    expect(mask.readAlpha(270, 270)).toBe(255)
    expect(mask.allocatedTileCount).toBe(1)

    alpha.fill(255)
    mask.replaceTiles([{ tileX: 0, tileY: 0, width: 256, height: 256, alpha }])
    expect(mask.allocatedTileCount).toBe(0)
  })

  it('rejects full-canvas allocation and concurrent strokes', () => {
    expect(() => new RasterMaskBuffer(8192, 8192)).toThrow(
      'invalid raster mask dimensions',
    )
    const mask = new RasterMaskBuffer(100, 100)
    const stroke = mask.beginStroke(hardEraser, { x: 10, y: 10 })
    expect(() => mask.beginStroke(hardEraser, { x: 20, y: 20 })).toThrow(
      'already active',
    )
    stroke.cancel()
  })
})

describe('RasterMaskHistory', () => {
  it('undoes and redoes one pointer gesture as one command', () => {
    const mask = new RasterMaskBuffer(300, 300)
    const history = new RasterMaskHistory()
    const stroke = mask.beginStroke(hardEraser, { x: 20, y: 20 })
    stroke.add({ x: 280, y: 280 })
    const patch = stroke.commit()
    history.commit(patch)

    expect(history.undo(mask)).toBeDefined()
    expect(mask.allocatedTileCount).toBe(0)
    expect(history.redo(mask)).toBeDefined()
    expect(mask.readAlpha(150, 150)).toBe(0)
  })

  it('enforces both entry and retained-byte budgets', () => {
    const mask = new RasterMaskBuffer(100, 100)
    const first = mask
      .beginStroke({ ...hardEraser, size: 4 }, { x: 10, y: 10 })
      .commit()
    const second = mask
      .beginStroke({ ...hardEraser, size: 4 }, { x: 30, y: 30 })
      .commit()
    const history = new RasterMaskHistory({
      maxEntries: 1,
      maxBytes: first.byteSize + second.byteSize,
    })
    expect(history.commit(first)).toBe(true)
    expect(history.commit(second)).toBe(true)
    expect(history.retainedBytes).toBe(second.byteSize)
    expect(history.undo(mask)).toBeDefined()
    expect(history.undo(mask)).toBeUndefined()
  })

  it('does not retain a command larger than its memory budget', () => {
    const mask = new RasterMaskBuffer(100, 100)
    const patch = mask.beginStroke(hardEraser, { x: 50, y: 50 }).commit()
    const history = new RasterMaskHistory({ maxBytes: patch.byteSize - 1 })
    expect(history.commit(patch)).toBe(false)
    expect(history.canUndo).toBe(false)
    expect(history.retainedBytes).toBe(0)
  })
})
