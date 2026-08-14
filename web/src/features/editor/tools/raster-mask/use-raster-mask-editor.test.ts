import { describe, expect, it } from 'vitest'

import { screenPointToRasterMask } from './use-raster-mask-editor'

describe('screenPointToRasterMask', () => {
  const bounds = {
    left: 100,
    top: 50,
    width: 800,
    height: 600,
  } as DOMRect

  it('maps screen coordinates through viewport and inverse node transform', () => {
    const point = screenPointToRasterMask(
      600,
      400,
      bounds,
      { zoom: 200, panX: 20, panY: -10 },
      [2, 0, 0, 2, 10, 5],
    )
    expect(point?.x).toBeCloseTo(15)
    expect(point?.y).toBeCloseTo(12.5)
  })

  it('rejects singular transforms instead of producing invalid brush points', () => {
    expect(
      screenPointToRasterMask(
        400,
        300,
        bounds,
        { zoom: 100, panX: 0, panY: 0 },
        [0, 0, 0, 1, 0, 0],
      ),
    ).toBeUndefined()
  })
})
