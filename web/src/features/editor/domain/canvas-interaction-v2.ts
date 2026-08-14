import type { EditorTransform } from './document'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import { multiplyTransforms } from '../renderer/scene-compiler'
import {
  invertAffine,
  objectBounds,
  transformPoint,
  unionBounds,
} from '#/lib/editor-transform'
import type { ObjectBounds } from '#/lib/editor-transform'

const identity: EditorTransform = [1, 0, 0, 1, 0, 0]

export type EditorAssetDimensions = ReadonlyMap<
  string,
  { width: number; height: number }
>

type RasterGeometry = {
  node: EditorNodeV2
  transform: EditorTransform
  visible: boolean
  locked: boolean
  order: number
}

export function hitTestEditorDocument(
  document: EditorDocumentV2,
  assets: EditorAssetDimensions,
  point: { x: number; y: number },
) {
  const geometry = flattenRasterGeometry(document)
  const geometryByID = new Map(geometry.map((entry) => [entry.node.id, entry]))
  const maskIDs = new Set(
    document.nodes.flatMap((node) => (node.mask_id ? [node.mask_id] : [])),
  )
  return geometry.reverse().find((entry) => {
    if (!entry.visible || maskIDs.has(entry.node.id)) return false
    if (!pointInsideRaster(entry, assets, point)) return false
    const mask = entry.node.mask_id
      ? geometryByID.get(entry.node.mask_id)
      : undefined
    return !mask || (mask.visible && pointInsideRaster(mask, assets, point))
  })?.node.id
}

export function editorSelectionBounds(
  document: EditorDocumentV2,
  assets: EditorAssetDimensions,
  selectedIDs: ReadonlySet<string>,
): ObjectBounds | undefined {
  const selectedRoots = editorSelectionRootIDs(document, selectedIDs)
  const geometry = flattenRasterGeometry(document)
  const geometryByID = new Map(geometry.map((entry) => [entry.node.id, entry]))
  const maskIDs = new Set(
    document.nodes.flatMap((node) => (node.mask_id ? [node.mask_id] : [])),
  )
  const byID = new Map(document.nodes.map((node) => [node.id, node]))
  const bounds: ObjectBounds[] = []
  for (const entry of geometry) {
    if (
      !entry.visible ||
      maskIDs.has(entry.node.id) ||
      !belongsToSelection(entry.node, byID, selectedRoots)
    )
      continue
    const contentBounds = rasterBounds(entry, assets)
    if (!contentBounds) continue
    const mask = entry.node.mask_id
      ? geometryByID.get(entry.node.mask_id)
      : undefined
    const maskBounds = mask?.visible ? rasterBounds(mask, assets) : undefined
    const visibleBounds = mask
      ? maskBounds && intersectBounds(contentBounds, maskBounds)
      : contentBounds
    if (visibleBounds) bounds.push(visibleBounds)
  }
  return unionBounds(bounds)
}

export function editorSelectionContainsNode(
  document: EditorDocumentV2,
  selectedIDs: ReadonlySet<string>,
  nodeID: string,
) {
  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  let current = nodes.get(nodeID)
  while (current) {
    if (selectedIDs.has(current.id)) return true
    current = current.parent_id ? nodes.get(current.parent_id) : undefined
  }
  return false
}

export function translateEditorNodes(
  document: EditorDocumentV2,
  selectedIDs: ReadonlySet<string>,
  worldDelta: { x: number; y: number },
): EditorDocumentV2 {
  if (
    selectedIDs.size === 0 ||
    !Number.isFinite(worldDelta.x) ||
    !Number.isFinite(worldDelta.y)
  )
    return document
  const byID = new Map(document.nodes.map((node) => [node.id, node]))
  const roots = editorSelectionRootIDs(document, selectedIDs).filter(
    (id) => byID.get(id)?.type !== 'adjustment',
  )
  if (roots.length === 0) return document
  const rootSet = new Set(roots)
  for (const id of roots) {
    if (editorNodeWorldAppearance(byID, id).locked)
      throw new TypeError('Locked editor nodes cannot be moved')
  }
  const result: EditorDocumentV2 = {
    ...document,
    nodes: document.nodes.map((node) => {
      if (!rootSet.has(node.id)) return node
      const parentTransform = node.parent_id
        ? editorNodeWorldTransform(byID, node.parent_id)
        : identity
      const inverse = invertAffine(parentTransform)
      if (!inverse) throw new TypeError('Parent transform is not invertible')
      const dx = inverse[0] * worldDelta.x + inverse[2] * worldDelta.y
      const dy = inverse[1] * worldDelta.x + inverse[3] * worldDelta.y
      return {
        ...node,
        transform: [
          node.transform[0],
          node.transform[1],
          node.transform[2],
          node.transform[3],
          node.transform[4] + dx,
          node.transform[5] + dy,
        ],
      }
    }),
  }
  return result
}

export function transformEditorNodesAroundWorldPoint(
  document: EditorDocumentV2,
  selectedIDs: ReadonlySet<string>,
  center: { x: number; y: number },
  operation:
    { type: 'scale'; factor: number } | { type: 'rotate'; degrees: number },
): EditorDocumentV2 {
  if (
    selectedIDs.size === 0 ||
    !Number.isFinite(center.x) ||
    !Number.isFinite(center.y) ||
    (operation.type === 'scale' &&
      (!Number.isFinite(operation.factor) || operation.factor <= 0)) ||
    (operation.type === 'rotate' && !Number.isFinite(operation.degrees))
  )
    return document
  const byID = new Map(document.nodes.map((node) => [node.id, node]))
  const roots = editorSelectionRootIDs(document, selectedIDs).filter(
    (id) => byID.get(id)?.type !== 'adjustment',
  )
  if (roots.length === 0) return document
  const rootSet = new Set(roots)
  for (const id of roots) {
    if (editorNodeWorldAppearance(byID, id).locked)
      throw new TypeError('Locked editor nodes cannot be transformed')
  }
  const radians =
    operation.type === 'rotate' ? (operation.degrees * Math.PI) / 180 : 0
  const cosine = Math.cos(radians)
  const sine = Math.sin(radians)
  const worldOperation: EditorTransform =
    operation.type === 'scale'
      ? [operation.factor, 0, 0, operation.factor, 0, 0]
      : [cosine, sine, -sine, cosine, 0, 0]
  const aroundCenter = multiplyTransforms(
    multiplyTransforms([1, 0, 0, 1, center.x, center.y], worldOperation),
    [1, 0, 0, 1, -center.x, -center.y],
  )
  const result: EditorDocumentV2 = {
    ...document,
    nodes: document.nodes.map((node) => {
      if (!rootSet.has(node.id)) return node
      const parentTransform = node.parent_id
        ? editorNodeWorldTransform(byID, node.parent_id)
        : identity
      const inverseParent = invertAffine(parentTransform)
      if (!inverseParent)
        throw new TypeError('Parent transform is not invertible')
      const world = editorNodeWorldTransform(byID, node.id)
      const transform = multiplyTransforms(
        inverseParent,
        multiplyTransforms(aroundCenter, world),
      )
      if (!validEditorTransform(transform))
        throw new TypeError('Editor transform exceeds the document limits')
      return {
        ...node,
        transform,
      }
    }),
  }
  return result
}

export function editorNodeWorldTransform(
  nodes: ReadonlyMap<string, EditorNodeV2>,
  nodeID: string,
): EditorTransform {
  const lineage: EditorNodeV2[] = []
  let current = nodes.get(nodeID)
  while (current) {
    lineage.unshift(current)
    current = current.parent_id ? nodes.get(current.parent_id) : undefined
  }
  return lineage.reduce<EditorTransform>(
    (result, node) => multiplyTransforms(result, node.transform),
    identity,
  )
}

export function editorNodeWorldAppearance(
  nodes: ReadonlyMap<string, EditorNodeV2>,
  nodeID: string,
) {
  let visible = true
  let locked = false
  let current = nodes.get(nodeID)
  while (current) {
    visible = visible && current.visible
    locked = locked || current.locked
    current = current.parent_id ? nodes.get(current.parent_id) : undefined
  }
  return { visible, locked }
}

function flattenRasterGeometry(document: EditorDocumentV2) {
  const children = new Map<string | null, EditorNodeV2[]>()
  for (const node of document.nodes) {
    const siblings = children.get(node.parent_id) ?? []
    siblings.push(node)
    children.set(node.parent_id, siblings)
  }
  for (const siblings of children.values()) siblings.sort(compareNodes)
  const result: RasterGeometry[] = []
  let order = 0
  const visit = (
    parentID: string | null,
    parentTransform: EditorTransform,
    parentVisible: boolean,
    parentLocked: boolean,
  ) => {
    for (const node of children.get(parentID) ?? []) {
      const transform = multiplyTransforms(parentTransform, node.transform)
      const visible = parentVisible && node.visible
      const locked = parentLocked || node.locked
      if (node.type === 'group') {
        visit(node.id, transform, visible, locked)
      } else if (node.type === 'raster') {
        result.push({ node, transform, visible, locked, order })
        order += 1
      }
    }
  }
  visit(null, identity, true, false)
  return result
}

function pointInsideRaster(
  entry: RasterGeometry,
  assets: EditorAssetDimensions,
  point: { x: number; y: number },
) {
  const asset = entry.node.asset_id
    ? assets.get(entry.node.asset_id)
    : undefined
  const inverse = invertAffine(entry.transform)
  if (!asset || !inverse) return false
  const local = transformPoint(inverse, point.x, point.y)
  const crop = entry.node.crop ?? { x: 0, y: 0, width: 1, height: 1 }
  const left = crop.x * asset.width
  const top = crop.y * asset.height
  const insideRaster =
    local.x >= left &&
    local.x <= left + crop.width * asset.width &&
    local.y >= top &&
    local.y <= top + crop.height * asset.height
  if (!insideRaster || !entry.node.shape_mask) return insideRaster
  const normalizedX = local.x / asset.width
  const normalizedY = local.y / asset.height
  const mask = entry.node.shape_mask
  const insideMask =
    mask.type === 'ellipse'
      ? Math.pow(
          (normalizedX - (mask.x + mask.width / 2)) / (mask.width / 2),
          2,
        ) +
          Math.pow(
            (normalizedY - (mask.y + mask.height / 2)) / (mask.height / 2),
            2,
          ) <=
        1
      : normalizedX >= mask.x &&
        normalizedX <= mask.x + mask.width &&
        normalizedY >= mask.y &&
        normalizedY <= mask.y + mask.height
  return insideMask !== mask.inverted
}

function rasterBounds(entry: RasterGeometry, assets: EditorAssetDimensions) {
  const asset = entry.node.asset_id
    ? assets.get(entry.node.asset_id)
    : undefined
  if (!asset) return undefined
  if (entry.node.shape_mask && !entry.node.shape_mask.inverted) {
    const mask = entry.node.shape_mask
    return objectBounds(
      multiplyTransforms(entry.transform, [
        1,
        0,
        0,
        1,
        mask.x * asset.width,
        mask.y * asset.height,
      ]),
      mask.width * asset.width,
      mask.height * asset.height,
    )
  }
  const crop = entry.node.crop ?? { x: 0, y: 0, width: 1, height: 1 }
  const transform = multiplyTransforms(entry.transform, [
    1,
    0,
    0,
    1,
    crop.x * asset.width,
    crop.y * asset.height,
  ])
  return objectBounds(
    transform,
    crop.width * asset.width,
    crop.height * asset.height,
  )
}

function intersectBounds(left: ObjectBounds, right: ObjectBounds) {
  const minimumX = Math.max(left.left, right.left)
  const minimumY = Math.max(left.top, right.top)
  const maximumX = Math.min(left.right, right.right)
  const maximumY = Math.min(left.bottom, right.bottom)
  if (maximumX <= minimumX || maximumY <= minimumY) return undefined
  return {
    left: minimumX,
    top: minimumY,
    right: maximumX,
    bottom: maximumY,
    centerX: (minimumX + maximumX) / 2,
    centerY: (minimumY + maximumY) / 2,
    width: maximumX - minimumX,
    height: maximumY - minimumY,
  } satisfies ObjectBounds
}

export function editorSelectionRootIDs(
  document: EditorDocumentV2,
  selectedIDs: ReadonlySet<string>,
) {
  const byID = new Map(document.nodes.map((node) => [node.id, node]))
  return document.nodes
    .filter((node) => selectedIDs.has(node.id))
    .filter((node) => {
      let parentID = node.parent_id
      while (parentID) {
        if (selectedIDs.has(parentID)) return false
        parentID = byID.get(parentID)?.parent_id ?? null
      }
      return true
    })
    .map((node) => node.id)
}

function belongsToSelection(
  node: EditorNodeV2,
  nodes: ReadonlyMap<string, EditorNodeV2>,
  selectedRoots: readonly string[],
) {
  const selected = new Set(selectedRoots)
  let current: EditorNodeV2 | undefined = node
  while (current) {
    if (selected.has(current.id)) return true
    current = current.parent_id ? nodes.get(current.parent_id) : undefined
  }
  return false
}

function compareNodes(left: EditorNodeV2, right: EditorNodeV2) {
  return (
    left.order_key.localeCompare(right.order_key) ||
    left.id.localeCompare(right.id)
  )
}

function validEditorTransform(transform: EditorTransform) {
  return (
    transform.every(
      (value) => Number.isFinite(value) && Math.abs(value) <= 1_000_000,
    ) &&
    Math.abs(transform[0] * transform[3] - transform[1] * transform[2]) >= 1e-8
  )
}
