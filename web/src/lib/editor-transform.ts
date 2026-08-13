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

export function scaleAroundCenter(
  object: EditorObject,
  width: number,
  height: number,
  target: number,
): EditorObject {
  const current = objectScale(object.transform)
  if (!Number.isFinite(target) || current < 1e-8) return object
  const factor = target / current
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

export function objectRotation(transform: Affine): number {
  return (Math.atan2(transform[1], transform[0]) * 180) / Math.PI
}

export function transformPoint(transform: Affine, x: number, y: number) {
  return {
    x: transform[0] * x + transform[2] * y + transform[4],
    y: transform[1] * x + transform[3] * y + transform[5],
  }
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
