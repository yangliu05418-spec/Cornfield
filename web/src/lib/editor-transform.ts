import type { EditorObject } from './api'

export type Affine = EditorObject['transform']

export function multiplyAffine(left: Affine, right: Affine): Affine {
  const [a, b, c, d, e, f] = left
  const [g, h, i, j, k, l] = right
  return [
    a * g + c * h,
    b * g + d * h,
    a * i + c * j,
    b * i + d * j,
    a * k + c * l + e,
    b * k + d * l + f,
  ]
}

export function aroundObjectCenter(
  transform: Affine,
  width: number,
  height: number,
  localTransform: Affine,
): Affine {
  const centerX = width / 2
  const centerY = height / 2
  return multiplyAffine(
    multiplyAffine(
      multiplyAffine(transform, [1, 0, 0, 1, centerX, centerY]),
      localTransform,
    ),
    [1, 0, 0, 1, -centerX, -centerY],
  )
}

export function rotateAroundCenter(
  object: EditorObject,
  width: number,
  height: number,
  degrees: number,
): EditorObject {
  const radians = (degrees * Math.PI) / 180
  const cos = Math.cos(radians)
  const sin = Math.sin(radians)
  return {
    ...object,
    transform: aroundObjectCenter(object.transform, width, height, [
      cos,
      sin,
      -sin,
      cos,
      0,
      0,
    ]),
  }
}

export function flipAroundCenter(
  object: EditorObject,
  width: number,
  height: number,
  axis: 'horizontal' | 'vertical',
): EditorObject {
  return {
    ...object,
    transform: aroundObjectCenter(
      object.transform,
      width,
      height,
      axis === 'horizontal' ? [-1, 0, 0, 1, 0, 0] : [1, 0, 0, -1, 0, 0],
    ),
  }
}

export function objectScale(transform: Affine): number {
  const [a, b, c, d] = transform
  return Math.sqrt(Math.abs(a * d - b * c))
}

export function objectAxisScales(transform: Affine) {
  return {
    x: Math.hypot(transform[0], transform[1]),
    y: Math.hypot(transform[2], transform[3]),
  }
}

export function scaleByFactorAroundCenter(
  object: EditorObject,
  width: number,
  height: number,
  factor: number,
): EditorObject {
  if (!Number.isFinite(factor) || factor <= 0) return object
  return {
    ...object,
    transform: aroundObjectCenter(object.transform, width, height, [
      factor,
      0,
      0,
      factor,
      0,
      0,
    ]),
  }
}

export function scaleAroundCenter(
  object: EditorObject,
  width: number,
  height: number,
  target: number,
): EditorObject {
  const current = objectScale(object.transform)
  if (!Number.isFinite(target) || current < 1e-8) return object
  const factor = target / current
  return scaleByFactorAroundCenter(object, width, height, factor)
}

export function objectRotation(transform: Affine): number {
  return (Math.atan2(transform[1], transform[0]) * 180) / Math.PI
}

export function transformPoint(transform: Affine, x: number, y: number) {
  return {
    x: transform[0] * x + transform[2] * y + transform[4],
    y: transform[1] * x + transform[3] * y + transform[5],
  }
}

export type ObjectBounds = {
  left: number
  top: number
  right: number
  bottom: number
  centerX: number
  centerY: number
  width: number
  height: number
}

export function objectBounds(
  transform: Affine,
  width: number,
  height: number,
): ObjectBounds {
  const corners = [
    transformPoint(transform, 0, 0),
    transformPoint(transform, width, 0),
    transformPoint(transform, width, height),
    transformPoint(transform, 0, height),
  ]
  const left = Math.min(...corners.map((point) => point.x))
  const right = Math.max(...corners.map((point) => point.x))
  const top = Math.min(...corners.map((point) => point.y))
  const bottom = Math.max(...corners.map((point) => point.y))
  return {
    left,
    top,
    right,
    bottom,
    centerX: (left + right) / 2,
    centerY: (top + bottom) / 2,
    width: right - left,
    height: bottom - top,
  }
}

export function moveObjectCenter(
  object: EditorObject,
  width: number,
  height: number,
  x: number,
  y: number,
): EditorObject {
  const center = transformPoint(object.transform, width / 2, height / 2)
  return {
    ...object,
    transform: [
      object.transform[0],
      object.transform[1],
      object.transform[2],
      object.transform[3],
      object.transform[4] + x - center.x,
      object.transform[5] + y - center.y,
    ],
  }
}

export type SnapGuide = { axis: 'x' | 'y'; position: number }

export function unionBounds(bounds: ObjectBounds[]): ObjectBounds | undefined {
  if (!bounds.length) return undefined
  const left = Math.min(...bounds.map((item) => item.left))
  const top = Math.min(...bounds.map((item) => item.top))
  const right = Math.max(...bounds.map((item) => item.right))
  const bottom = Math.max(...bounds.map((item) => item.bottom))
  return {
    left,
    top,
    right,
    bottom,
    centerX: (left + right) / 2,
    centerY: (top + bottom) / 2,
    width: right - left,
    height: bottom - top,
  }
}

export function boundsIntersect(left: ObjectBounds, right: ObjectBounds) {
  return !(
    left.right < right.left ||
    left.left > right.right ||
    left.bottom < right.top ||
    left.top > right.bottom
  )
}

export function snapBoundsTranslation(
  bounds: ObjectBounds,
  targets: ObjectBounds[],
  threshold: number,
) {
  const xPoints = [bounds.left, bounds.centerX, bounds.right]
  const yPoints = [bounds.top, bounds.centerY, bounds.bottom]
  const xTargets = targets.flatMap((target) => [
    target.left,
    target.centerX,
    target.right,
  ])
  const yTargets = targets.flatMap((target) => [
    target.top,
    target.centerY,
    target.bottom,
  ])
  const xSnap = nearestSnap(xPoints, xTargets, threshold)
  const ySnap = nearestSnap(yPoints, yTargets, threshold)
  return {
    dx: xSnap?.delta ?? 0,
    dy: ySnap?.delta ?? 0,
    guides: [
      ...(xSnap ? [{ axis: 'x' as const, position: xSnap.position }] : []),
      ...(ySnap ? [{ axis: 'y' as const, position: ySnap.position }] : []),
    ],
  }
}

export function snapObjectTranslation(
  transform: Affine,
  width: number,
  height: number,
  targets: ObjectBounds[],
  threshold: number,
) {
  const bounds = objectBounds(transform, width, height)
  return snapBoundsTranslation(bounds, targets, threshold)
}

function nearestSnap(points: number[], targets: number[], threshold: number) {
  let best: { delta: number; position: number } | undefined
  for (const point of points) {
    for (const target of targets) {
      const delta = target - point
      if (
        Math.abs(delta) <= threshold &&
        (!best || Math.abs(delta) < Math.abs(best.delta))
      )
        best = { delta, position: target }
    }
  }
  return best
}

export function screenPointToWorld(
  clientX: number,
  clientY: number,
  viewport: DOMRect,
  view: { zoom: number; panX: number; panY: number },
) {
  const scale = view.zoom / 100
  return {
    x: (clientX - viewport.left - viewport.width / 2 - view.panX) / scale,
    y: (clientY - viewport.top - viewport.height / 2 - view.panY) / scale,
  }
}

export function zoomAtScreenPoint(
  view: { zoom: number; panX: number; panY: number },
  nextZoom: number,
  clientX: number,
  clientY: number,
  viewport: DOMRect,
) {
  const world = screenPointToWorld(clientX, clientY, viewport, view)
  const scale = nextZoom / 100
  const offsetX = clientX - viewport.left - viewport.width / 2
  const offsetY = clientY - viewport.top - viewport.height / 2
  return {
    zoom: nextZoom,
    panX: offsetX - world.x * scale,
    panY: offsetY - world.y * scale,
  }
}

export function fitArtboard(
  viewportWidth: number,
  viewportHeight: number,
  artboardWidth: number,
  artboardHeight: number,
  padding = 96,
) {
  const availableWidth = Math.max(1, viewportWidth - padding * 2)
  const availableHeight = Math.max(1, viewportHeight - padding * 2)
  const zoom = Math.min(
    100,
    (availableWidth / artboardWidth) * 100,
    (availableHeight / artboardHeight) * 100,
  )
  const scale = zoom / 100
  return {
    zoom,
    panX: (-artboardWidth * scale) / 2,
    panY: (-artboardHeight * scale) / 2,
  }
}
