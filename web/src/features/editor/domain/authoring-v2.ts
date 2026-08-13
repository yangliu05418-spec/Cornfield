import type { EditorTransform } from './document'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import { validateEditorDocumentV2 } from './document-v2'
import { multiplyTransforms } from '../renderer/scene-compiler'

const identity: EditorTransform = [1, 0, 0, 1, 0, 0]

export class EditorCommandError extends Error {
  constructor(
    readonly code:
      | 'INVALID_DOCUMENT'
      | 'INVALID_SELECTION'
      | 'INVALID_PARENT'
      | 'INVALID_MASK'
      | 'CYCLE',
    message: string,
  ) {
    super(message)
    this.name = 'EditorCommandError'
  }
}

export type EditorLayerTreeNode = {
  node: EditorNodeV2
  depth: number
  children: EditorLayerTreeNode[]
}

export function buildEditorLayerTree(
  document: EditorDocumentV2,
): EditorLayerTreeNode[] {
  assertValid(document)
  const children = childrenByParent(document)
  const visit = (
    parentID: string | null,
    depth: number,
  ): EditorLayerTreeNode[] =>
    (children.get(parentID) ?? []).map<EditorLayerTreeNode>((node) => ({
      node,
      depth,
      children: visit(node.id, depth + 1),
    }))
  return visit(null, 0)
}

export function groupEditorNodes(
  document: EditorDocumentV2,
  nodeIDs: readonly string[],
  group: { id: string; name?: string },
): EditorDocumentV2 {
  assertValid(document)
  const selected = uniqueNodes(document, nodeIDs)
  if (
    selected.length < 1 ||
    document.nodes.some((node) => node.id === group.id)
  )
    throw commandError('INVALID_SELECTION', 'Select at least one unique node')
  const parentID = selected[0].parent_id
  if (selected.some((node) => node.parent_id !== parentID))
    throw commandError(
      'INVALID_SELECTION',
      'Nodes must share a parent before grouping',
    )
  const selectedIDs = new Set(selected.map((node) => node.id))
  for (const node of document.nodes) {
    if (
      node.type === 'adjustment' &&
      node.target_id &&
      selectedIDs.has(node.target_id)
    )
      selectedIDs.add(node.id)
  }
  if (
    document.nodes.some(
      (node) =>
        node.type === 'adjustment' &&
        selectedIDs.has(node.id) &&
        (!node.target_id || !selectedIDs.has(node.target_id)),
    )
  )
    throw commandError(
      'INVALID_SELECTION',
      'Group an adjustment layer together with its target',
    )
  if (
    document.nodes.some(
      (node) =>
        node.mask_id &&
        selectedIDs.has(node.mask_id) !== selectedIDs.has(node.id),
    )
  )
    throw commandError(
      'INVALID_SELECTION',
      'Content and its mask must be grouped together',
    )

  const ordered = siblings(document, parentID)
  const firstIndex = Math.min(
    ...selected.map((node) => ordered.findIndex((item) => item.id === node.id)),
  )
  const groupNode: EditorNodeV2 = {
    id: group.id,
    type: 'group',
    name: group.name,
    parent_id: parentID,
    order_key: orderKey(firstIndex),
    transform: [...identity],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
  }
  const next = cloneDocument(document)
  next.nodes = next.nodes.map((node) =>
    selectedIDs.has(node.id) ? { ...node, parent_id: group.id } : node,
  )
  next.nodes.push(groupNode)
  const parentOrder = ordered.flatMap((node, index) => {
    if (index === firstIndex) return [group.id]
    return selectedIDs.has(node.id) ? [] : [node.id]
  })
  return normalizeEditorOrderKeys(next, {
    parentID,
    preferredOrder: parentOrder,
  })
}

export function ungroupEditorNode(
  document: EditorDocumentV2,
  groupID: string,
): EditorDocumentV2 {
  assertValid(document)
  const group = requireNode(document, groupID)
  if (group.type !== 'group')
    throw commandError('INVALID_SELECTION', 'Only groups can be ungrouped')
  if (group.mask_id)
    throw commandError('INVALID_MASK', 'Group masks are not supported')
  const children = siblings(document, group.id)
  const parentSiblings = siblings(document, group.parent_id)
  const groupIndex = parentSiblings.findIndex((node) => node.id === group.id)
  const childIDs = new Set(children.map((node) => node.id))
  const next = cloneDocument(document)
  next.nodes = next.nodes
    .filter((node) => node.id !== group.id)
    .map((node) =>
      childIDs.has(node.id)
        ? {
            ...node,
            parent_id: group.parent_id,
            transform:
              node.type === 'adjustment'
                ? [...identity]
                : multiplyTransforms(group.transform, node.transform),
            opacity:
              node.type === 'adjustment'
                ? node.opacity
                : group.opacity * node.opacity,
            visible: group.visible && node.visible,
            locked: group.locked || node.locked,
          }
        : node,
    )
  return normalizeEditorOrderKeys(next, {
    parentID: group.parent_id,
    preferredOrder: [
      ...parentSiblings.slice(0, groupIndex).map((node) => node.id),
      ...children.map((node) => node.id),
      ...parentSiblings.slice(groupIndex + 1).map((node) => node.id),
    ],
  })
}

export function attachEditorMask(
  document: EditorDocumentV2,
  contentID: string,
  maskID: string,
): EditorDocumentV2 {
  assertValid(document)
  const content = requireNode(document, contentID)
  const mask = requireNode(document, maskID)
  if (
    content.id === mask.id ||
    content.type !== 'raster' ||
    mask.type !== 'raster' ||
    content.parent_id !== mask.parent_id ||
    mask.mask_id !== undefined ||
    mask.crop !== undefined ||
    document.nodes.some((node) => node.mask_id === mask.id)
  )
    throw commandError(
      'INVALID_MASK',
      'Mask must be an unused, uncropped raster sibling',
    )
  const ordered = siblings(document, content.parent_id).filter(
    (node) => node.id !== maskID,
  )
  const contentIndex = ordered.findIndex((node) => node.id === contentID)
  ordered.splice(Math.max(0, contentIndex), 0, mask)
  const next = cloneDocument(document)
  next.nodes = next.nodes.map((node) =>
    node.id === contentID ? { ...node, mask_id: maskID } : node,
  )
  return normalizeEditorOrderKeys(next, {
    parentID: content.parent_id,
    preferredOrder: ordered.map((node) => node.id),
  })
}

export function detachEditorMask(
  document: EditorDocumentV2,
  contentID: string,
): EditorDocumentV2 {
  assertValid(document)
  const content = requireNode(document, contentID)
  if (!content.mask_id) return document
  const next = cloneDocument(document)
  next.nodes = next.nodes.map((node) => {
    if (node.id !== contentID) return node
    const { mask_id: _maskID, ...withoutMask } = node
    return withoutMask
  })
  return next
}

export function removeEditorNodes(
  document: EditorDocumentV2,
  nodeIDs: readonly string[],
): EditorDocumentV2 {
  assertValid(document)
  const selected = uniqueNodes(document, nodeIDs)
  if (selected.length < 1)
    throw commandError('INVALID_SELECTION', 'Select at least one node')
  const removed = new Set(selected.map((node) => node.id))
  let changed = true
  while (changed) {
    changed = false
    for (const node of document.nodes) {
      if (
        !removed.has(node.id) &&
        ((node.parent_id !== null && removed.has(node.parent_id)) ||
          (node.type === 'adjustment' &&
            node.target_id !== undefined &&
            removed.has(node.target_id)))
      ) {
        removed.add(node.id)
        changed = true
      }
    }
  }
  const next = cloneDocument(document)
  next.nodes = next.nodes
    .filter((node) => !removed.has(node.id))
    .map((node) =>
      node.mask_id && removed.has(node.mask_id)
        ? omitEditorMaskReference(node)
        : node,
    )
  return normalizeEditorOrderKeys(next)
}

export function reparentEditorNodes(
  document: EditorDocumentV2,
  nodeIDs: readonly string[],
  parentID: string | null,
  targetIndex?: number,
): EditorDocumentV2 {
  assertValid(document)
  const selected = uniqueNodes(document, nodeIDs)
  if (selected.length < 1)
    throw commandError('INVALID_SELECTION', 'Select at least one node')
  if (parentID !== null && requireNode(document, parentID).type !== 'group')
    throw commandError('INVALID_PARENT', 'Parent must be a group')
  const moving = new Set(selected.map((node) => node.id))
  for (const node of document.nodes) {
    if (
      node.type === 'adjustment' &&
      node.target_id &&
      moving.has(node.target_id)
    )
      moving.add(node.id)
  }
  for (const node of selected) {
    if (hasSelectedAncestor(document, node, moving))
      throw commandError(
        'INVALID_SELECTION',
        'Select a parent or its descendants, not both',
      )
    if (
      parentID === node.id ||
      (parentID && isDescendant(document, parentID, node.id))
    )
      throw commandError('CYCLE', 'A node cannot be moved into its descendant')
    if (
      node.mask_id &&
      !moving.has(node.mask_id) &&
      requireNode(document, node.mask_id).parent_id !== parentID
    )
      throw commandError('INVALID_MASK', 'Move content and its mask together')
    if (
      document.nodes.some(
        (candidate) =>
          candidate.mask_id === node.id &&
          !moving.has(candidate.id) &&
          candidate.parent_id !== parentID,
      )
    )
      throw commandError('INVALID_MASK', 'Move content and its mask together')
    if (
      node.type === 'adjustment' &&
      node.target_id &&
      parentID !== node.parent_id &&
      !moving.has(node.target_id)
    )
      throw commandError(
        'INVALID_SELECTION',
        'Move an adjustment layer together with its target',
      )
  }

  const parentWorld = parentID ? worldTransform(document, parentID) : identity
  const parentAppearance = parentID
    ? worldAppearance(document, parentID)
    : { opacity: 1, visible: true, locked: false }
  const inverseParent = invertTransform(parentWorld)
  const next = cloneDocument(document)
  next.nodes = next.nodes.map((node) => {
    if (!moving.has(node.id)) return node
    const world = worldTransform(document, node.id)
    const appearance = worldAppearance(document, node.id)
    if (
      (node.type !== 'adjustment' && parentAppearance.opacity <= 0) ||
      (node.type !== 'adjustment' &&
        (appearance.opacity / parentAppearance.opacity > 1 + 1e-9 ||
          (appearance.visible && !parentAppearance.visible) ||
          (!appearance.locked && parentAppearance.locked)))
    )
      throw commandError(
        'INVALID_PARENT',
        'Destination group cannot preserve the node appearance',
      )
    return {
      ...node,
      parent_id: parentID,
      transform:
        node.type === 'adjustment'
          ? [...identity]
          : multiplyTransforms(inverseParent, world),
      opacity:
        node.type === 'adjustment'
          ? node.opacity
          : Math.min(1, appearance.opacity / parentAppearance.opacity),
      visible: node.type === 'adjustment' ? node.visible : appearance.visible,
      locked: node.type === 'adjustment' ? node.locked : appearance.locked,
    }
  })
  const destination = siblings(next, parentID).filter(
    (node) => !moving.has(node.id),
  )
  const insertAt = Math.max(
    0,
    Math.min(targetIndex ?? destination.length, destination.length),
  )
  destination.splice(
    insertAt,
    0,
    ...document.nodes
      .filter((node) => moving.has(node.id))
      .sort(compareNodes)
      .map((node) => requireNode(next, node.id)),
  )
  return normalizeEditorOrderKeys(next, {
    parentID,
    preferredOrder: destination.map((node) => node.id),
  })
}

export function reorderEditorNode(
  document: EditorDocumentV2,
  nodeID: string,
  targetIndex: number,
): EditorDocumentV2 {
  const node = requireNode(document, nodeID)
  return reparentEditorNodes(document, [nodeID], node.parent_id, targetIndex)
}

export function normalizeEditorOrderKeys(
  document: EditorDocumentV2,
  preference?: { parentID: string | null; preferredOrder: readonly string[] },
): EditorDocumentV2 {
  const next = cloneDocument(document)
  const children = childrenByParent(next)
  if (preference) {
    const preferred = new Map(
      preference.preferredOrder.map((id, index) => [id, index]),
    )
    const values = children.get(preference.parentID) ?? []
    values.sort((left, right) => {
      const leftIndex = preferred.get(left.id)
      const rightIndex = preferred.get(right.id)
      if (leftIndex !== undefined && rightIndex !== undefined)
        return leftIndex - rightIndex
      if (leftIndex !== undefined) return -1
      if (rightIndex !== undefined) return 1
      return compareNodes(left, right)
    })
  }
  const keys = new Map<string, string>()
  for (const values of children.values())
    values.forEach((node, index) => keys.set(node.id, orderKey(index)))
  next.nodes = next.nodes.map((node) => ({
    ...node,
    order_key: keys.get(node.id) ?? node.order_key,
  }))
  assertValid(next)
  return next
}

function worldTransform(
  document: EditorDocumentV2,
  nodeID: string,
): EditorTransform {
  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  const lineage: EditorNodeV2[] = []
  let current: EditorNodeV2 | undefined = nodes.get(nodeID)
  while (current) {
    lineage.unshift(current)
    current = current.parent_id ? nodes.get(current.parent_id) : undefined
  }
  return lineage.reduce<EditorTransform>(
    (result, node) => multiplyTransforms(result, node.transform),
    identity,
  )
}

function worldAppearance(document: EditorDocumentV2, nodeID: string) {
  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  let opacity = 1
  let visible = true
  let locked = false
  let current: EditorNodeV2 | undefined = nodes.get(nodeID)
  while (current) {
    opacity *= current.opacity
    visible = visible && current.visible
    locked = locked || current.locked
    current = current.parent_id ? nodes.get(current.parent_id) : undefined
  }
  return { opacity, visible, locked }
}

function invertTransform(transform: EditorTransform): EditorTransform {
  const determinant = transform[0] * transform[3] - transform[1] * transform[2]
  if (Math.abs(determinant) < 1e-8)
    throw commandError('INVALID_PARENT', 'Parent transform is not invertible')
  return [
    transform[3] / determinant,
    -transform[1] / determinant,
    -transform[2] / determinant,
    transform[0] / determinant,
    (transform[2] * transform[5] - transform[3] * transform[4]) / determinant,
    (transform[1] * transform[4] - transform[0] * transform[5]) / determinant,
  ]
}

function childrenByParent(document: EditorDocumentV2) {
  const children = new Map<string | null, EditorNodeV2[]>()
  for (const node of document.nodes) {
    const values = children.get(node.parent_id) ?? []
    values.push(node)
    children.set(node.parent_id, values)
  }
  for (const values of children.values()) values.sort(compareNodes)
  return children
}

function siblings(document: EditorDocumentV2, parentID: string | null) {
  return (childrenByParent(document).get(parentID) ?? []).slice()
}

function uniqueNodes(document: EditorDocumentV2, ids: readonly string[]) {
  return [...new Set(ids)].map((id) => requireNode(document, id))
}

function requireNode(document: EditorDocumentV2, id: string) {
  const node = document.nodes.find((candidate) => candidate.id === id)
  if (!node) throw commandError('INVALID_SELECTION', `Unknown node ${id}`)
  return node
}

function isDescendant(
  document: EditorDocumentV2,
  candidateID: string,
  ancestorID: string,
) {
  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  let current = nodes.get(candidateID)
  while (current?.parent_id) {
    if (current.parent_id === ancestorID) return true
    current = nodes.get(current.parent_id)
  }
  return false
}

function hasSelectedAncestor(
  document: EditorDocumentV2,
  node: EditorNodeV2,
  selected: ReadonlySet<string>,
) {
  const nodes = new Map(document.nodes.map((item) => [item.id, item]))
  let parentID = node.parent_id
  while (parentID) {
    if (selected.has(parentID)) return true
    parentID = nodes.get(parentID)?.parent_id ?? null
  }
  return false
}

function compareNodes(left: EditorNodeV2, right: EditorNodeV2) {
  return (
    left.order_key.localeCompare(right.order_key) ||
    left.id.localeCompare(right.id)
  )
}

function omitEditorMaskReference(node: EditorNodeV2): EditorNodeV2 {
  const { mask_id: _maskID, ...withoutMask } = node
  return withoutMask
}

function orderKey(index: number) {
  return index.toString().padStart(8, '0')
}

function cloneDocument(document: EditorDocumentV2): EditorDocumentV2 {
  return structuredClone(document)
}

function assertValid(document: EditorDocumentV2) {
  if (validateEditorDocumentV2(document).length > 0)
    throw commandError('INVALID_DOCUMENT', 'Invalid editor document')
}

function commandError(code: EditorCommandError['code'], message: string) {
  return new EditorCommandError(code, message)
}
