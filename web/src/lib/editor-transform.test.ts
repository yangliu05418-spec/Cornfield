import { describe, expect, it } from 'vitest'

import {
  fitArtboard,
  flipAroundCenter,
  boundsIntersect,
  moveObjectCenter,
  multiplyAffine,
  objectAxisScales,
  objectBounds,
  rotateAroundCenter,
  rotateAroundWorldPoint,
  scaleAroundCenter,
  scaleByFactorAroundCenter,
  scaleAroundWorldPoint,
  snapObjectTranslation,
  snapBoundsTranslation,
  transformPoint,
  unionBounds,
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

  it('transforms multiple object matrices around one world-space center', () => {
    const center = { x: 100, y: 100 }
    const first = [1, 0, 0, 1, 20, 40] as EditorObject['transform']
    const second = [0, 1, -1, 0, 180, 40] as EditorObject['transform']

    const scaledFirst = scaleAroundWorldPoint(first, center, 2)
    const scaledSecond = scaleAroundWorldPoint(second, center, 2)
    expect(transformPoint(scaledFirst, 0, 0)).toEqual({ x: -60, y: -20 })
    expect(transformPoint(scaledSecond, 0, 0)).toEqual({ x: 260, y: -20 })
    expect(objectAxisScales(scaledFirst)).toEqual({ x: 2, y: 2 })
    expect(objectAxisScales(scaledSecond)).toEqual({ x: 2, y: 2 })

    const rotatedFirst = rotateAroundWorldPoint(first, center, 90)
    const rotatedSecond = rotateAroundWorldPoint(second, center, 90)
    expect(transformPoint(rotatedFirst, 0, 0).x).toBeCloseTo(160)
    expect(transformPoint(rotatedFirst, 0, 0).y).toBeCloseTo(20)
    expect(transformPoint(rotatedSecond, 0, 0).x).toBeCloseTo(160)
    expect(transformPoint(rotatedSecond, 0, 0).y).toBeCloseTo(180)
  })

  it('measures non-uniform axes and applies a uniform factor', () => {
    const stretched: EditorObject = {
      ...object,
      transform: [2, 0, 0, 3, 10, 20],
    }
    expect(objectAxisScales(stretched.transform)).toEqual({ x: 2, y: 3 })
    const center = transformPoint(stretched.transform, 50, 25)
    const result = scaleByFactorAroundCenter(stretched, 100, 50, 0.5)
    expect(objectAxisScales(result.transform).x).toBeCloseTo(1)
    expect(objectAxisScales(result.transform).y).toBeCloseTo(1.5)
    expect(transformPoint(result.transform, 50, 25).x).toBeCloseTo(center.x)
    expect(transformPoint(result.transform, 50, 25).y).toBeCloseTo(center.y)
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

  it('reports rotated bounds and moves by the image center', () => {
    const rotated = rotateAroundCenter(object, 100, 50, 90)
    const bounds = objectBounds(rotated.transform, 100, 50)
    expect(bounds.width).toBeCloseTo(50)
    expect(bounds.height).toBeCloseTo(100)
    const moved = moveObjectCenter(rotated, 100, 50, 300, 200)
    expect(transformPoint(moved.transform, 50, 25)).toEqual({ x: 300, y: 200 })
  })

  it('snaps object edges and centers to the nearest target', () => {
    const result = snapObjectTranslation(
      [1, 0, 0, 1, 96, 195],
      100,
      50,
      [
        {
          left: 100,
          top: 0,
          right: 500,
          bottom: 400,
          centerX: 250,
          centerY: 200,
          width: 500,
          height: 400,
        },
      ],
      6,
    )
    expect(result).toEqual({
      dx: 4,
      dy: 5,
      guides: [
        { axis: 'x', position: 100 },
        { axis: 'y', position: 200 },
      ],
    })
  })

  it('combines and intersects group bounds for marquee selection', () => {
    const first = objectBounds([1, 0, 0, 1, 10, 20], 100, 50)
    const second = objectBounds([1, 0, 0, 1, 200, 100], 40, 80)
    const combined = unionBounds([first, second])
    expect(combined).toEqual({
      left: 10,
      top: 20,
      right: 240,
      bottom: 180,
      centerX: 125,
      centerY: 100,
      width: 230,
      height: 160,
    })
    expect(boundsIntersect(first, second)).toBe(false)
    expect(
      boundsIntersect(first, {
        left: 100,
        top: 40,
        right: 140,
        bottom: 80,
        centerX: 120,
        centerY: 60,
        width: 40,
        height: 40,
      }),
    ).toBe(true)
  })

  it('snaps a group bounding box without requiring a synthetic object', () => {
    const moving = objectBounds([1, 0, 0, 1, 96, 195], 100, 50)
    const target = objectBounds([1, 0, 0, 1, 100, 0], 400, 400)
    expect(snapBoundsTranslation(moving, [target], 6)).toEqual({
      dx: 4,
      dy: 5,
      guides: [
        { axis: 'x', position: 100 },
        { axis: 'y', position: 200 },
      ],
    })
  })
})
