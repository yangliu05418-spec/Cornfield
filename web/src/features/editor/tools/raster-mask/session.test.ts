import { describe, expect, it } from 'vitest'

import type { RasterMaskBrush } from './tile-mask'
import { RasterMaskSession } from './session'

const brush: RasterMaskBrush = {
  size: 20,
  hardness: 1,
  opacity: 1,
  spacing: 0.1,
  mode: 'erase',
  pressureSize: 0,
  pressureOpacity: 0,
}

describe('RasterMaskSession', () => {
  it('commits one gesture and returns its complete dirty tile set', () => {
    const session = new RasterMaskSession(1024, 1024)
    const first = session.beginStroke('pointer-1', brush, { x: 100, y: 100 })
    const preview = session.addPoints('pointer-1', [
      { x: 110, y: 110 },
      { x: 120, y: 120 },
    ])
    const mutation = session.commitStroke('pointer-1')

    expect(first.tiles).toHaveLength(1)
    expect(preview.tiles).toHaveLength(1)
    expect(preview.tiles[0]).toMatchObject({
      tileX: 0,
      tileY: 0,
      width: 256,
      height: 256,
    })
    expect(preview.tiles[0].alpha).toHaveLength(256 * 256)
    expect(mutation.tiles).toHaveLength(1)
    expect(mutation.changedPixels).toBeGreaterThan(0)
    expect(mutation.canUndo).toBe(true)
    expect(mutation.canRedo).toBe(false)
    expect(session.buffer.allocatedBytes).toBe(256 * 256)
  })

  it('hydrates an immutable sparse version and clears local history', () => {
    const session = new RasterMaskSession(512, 512)
    session.beginStroke('old', brush, { x: 10, y: 10 })
    session.commitStroke('old')
    const alpha = new Uint8Array(256 * 256)
    alpha.fill(64)
    const hydrated = session.hydrate([
      { tileX: 1, tileY: 0, width: 256, height: 256, alpha },
    ])
    expect(hydrated.tiles).toHaveLength(1)
    expect(hydrated.canUndo).toBe(false)
    expect(session.buffer.readAlpha(10, 10)).toBe(255)
    expect(session.buffer.readAlpha(300, 10)).toBe(64)
  })

  it('keeps patch ownership in the session across undo and redo', () => {
    const session = new RasterMaskSession(512, 512)
    session.beginStroke('gesture', brush, { x: 100, y: 100 })
    const committed = session.commitStroke('gesture')
    const erased = session.buffer.readAlpha(100, 100)

    const undone = session.undo()
    expect(undone.tiles).toHaveLength(1)
    expect(session.buffer.readAlpha(100, 100)).toBe(255)
    expect(undone.canRedo).toBe(true)
    const redone = session.redo()
    expect(redone.tiles).toHaveLength(1)
    expect(session.buffer.readAlpha(100, 100)).toBe(erased)
    expect(redone.retainedHistoryBytes).toBe(committed.retainedHistoryBytes)
  })

  it('rejects stale pointer messages and history changes during a gesture', () => {
    const session = new RasterMaskSession(512, 512)
    session.beginStroke('current', brush, { x: 10, y: 10 })
    expect(() => session.addPoints('stale', [{ x: 20, y: 20 }])).toThrow(
      'does not match',
    )
    expect(() => session.undo()).toThrow('during a stroke')
    session.cancelStroke('current')
  })

  it('reports when a valid mutation cannot fit the undo budget', () => {
    const session = new RasterMaskSession(512, 512, { historyBytes: 1 })
    session.beginStroke('large', brush, { x: 100, y: 100 })
    const mutation = session.commitStroke('large')
    expect(mutation.changedPixels).toBeGreaterThan(0)
    expect(mutation.undoRetained).toBe(false)
    expect(mutation.canUndo).toBe(false)
    expect(session.buffer.readAlpha(100, 100)).toBe(0)
  })
})
