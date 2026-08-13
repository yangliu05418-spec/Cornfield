import { describe, expect, it } from 'vitest'

import {
  fitArtboard,
  flipAroundCenter,
  multiplyAffine,
  rotateAroundCenter,
  scaleAroundCenter,
  transformPoint,
  zoomAtScreenPoint,
} from './editor-transform'
import type { EditorObject } from './api'

const object: EditorObject = {
  id: 'image',
  asset_id: 'asset',
  transform: [1, 0, 0, 1, 10, 20],
  opacity: 1,
  visible: true,
  locked: false,
  z_index: 0,
}

function point(matrix: EditorObject['transform'], x: number, y: number) {
  return {
    x: matrix[0] * x + matrix[2] * y + matrix[4],
    y: matrix[1] * x + matrix[3] * y + matrix[5],
  }
}

describe('editor affine transforms', () => {
  it('multiplies CSS matrices in application order', () => {
    expect(multiplyAffine([1, 0, 0, 1, 10, 20], [2, 0, 0, 2, 0, 0])).toEqual([
      2, 0, 0, 2, 10, 20,
    ])
  })

  it('rotates, scales, and flips around the image center', () => {
    const center = point(object.transform, 50, 25)
    for (const changed of [
      rotateAroundCenter(object, 100, 50, 90),
      scaleAroundCenter(object, 100, 50, 2),
      flipAroundCenter(object, 100, 50, 'horizontal'),
      flipAroundCenter(object, 100, 50, 'vertical'),
    ]) {
      expect(point(changed.transform, 50, 25).x).toBeCloseTo(center.x)
      expect(point(changed.transform, 50, 25).y).toBeCloseTo(center.y)
    }
  })

  it('fits an artboard and keeps wheel zoom anchored to the cursor', () => {
    const view = fitArtboard(1200, 800, 1000, 500)
    expect(view.zoom).toBe(100)
    const viewport = {
      left: 0,
      top: 0,
      width: 1200,
      height: 800,
    } as DOMRect
    const zoomed = zoomAtScreenPoint(view, 200, 600, 400, viewport)
    expect(zoomed.panX).toBe(-1000)
    expect(zoomed.panY).toBe(-500)
  })

  it('maps local coordinates through the CSS-compatible matrix', () => {
    expect(transformPoint([0, 2, -2, 0, 30, 40], 4, 6)).toEqual({
      x: 18,
      y: 48,
    })
  })
})
