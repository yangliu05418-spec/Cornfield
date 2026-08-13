import {
  buildEditorLayerTree,
  reparentEditorNodes,
  reorderEditorNode,
} from './authoring-v2'
import type { EditorLayerTreeNode } from './authoring-v2'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import { editorSelectionRootIDs } from './canvas-interaction-v2'

export type EditorLayerRow = {
  entry: EditorLayerTreeNode
  hasChildren: boolean
}

export function buildVisibleEditorLayerRows(
  document: EditorDocumentV2,
  collapsed: ReadonlySet<string>,
) {
  const result: EditorLayerRow[] = []
  const visit = (entries: EditorLayerTreeNode[]) => {
    // Higher order keys render above lower ones and therefore appear first in
    // the panel. Children stay immediately below their parent.
    for (const entry of [...entries].reverse()) {
      result.push({ entry, hasChildren: entry.children.length > 0 })
      if (!collapsed.has(entry.node.id)) visit(entry.children)
    }
  }
  visit(buildEditorLayerTree(document))
  return result
}

export function canGroupEditorNodes(nodes: EditorNodeV2[]) {
  const selected = new Set(nodes.map((node) => node.id))
  return (
    nodes.length > 0 &&
    nodes.every(
      (node) =>
        node.parent_id === nodes[0]?.parent_id &&
        (node.type !== 'adjustment' ||
          (node.target_id !== undefined && selected.has(node.target_id))),
    )
  )
}

export function canAttachEditorMask(
  nodes: EditorNodeV2[],
  active?: EditorNodeV2,
) {
  return (
    nodes.length === 2 &&
    active?.type === 'raster' &&
    nodes.every(
      (node) => node.type === 'raster' && node.parent_id === active.parent_id,
    )
  )
}

export function reorderEditorNodeRelative(
  document: EditorDocumentV2,
  nodeID: string,
  direction: -1 | 1,
) {
  const node = document.nodes.find((candidate) => candidate.id === nodeID)
  if (!node) return document
  const siblings = document.nodes
    .filter((candidate) => candidate.parent_id === node.parent_id)
    .sort(
      (left, right) =>
        left.order_key.localeCompare(right.order_key) ||
        left.id.localeCompare(right.id),
    )
  const index = siblings.findIndex((candidate) => candidate.id === nodeID)
  const target = Math.max(0, Math.min(siblings.length - 1, index + direction))
  return target === index
    ? document
    : reorderEditorNode(document, nodeID, target)
}

export type EditorLayerDropPosition = 'before' | 'inside' | 'after'

export function moveEditorNodesByDrop(
  document: EditorDocumentV2,
  nodeIDs: readonly string[],
  targetID: string,
  position: EditorLayerDropPosition,
) {
  const roots = editorSelectionRootIDs(document, new Set(nodeIDs))
  const expandedRoots = expandAdjustmentCompanions(document, roots)
  const moving = new Set(expandedRoots)
  const target = document.nodes.find((node) => node.id === targetID)
  if (!target || moving.has(target.id)) return document
  if (position === 'inside') {
    if (target.type !== 'group') return document
    const childCount = document.nodes.filter(
      (node) => node.parent_id === target.id && !moving.has(node.id),
    ).length
    return reparentEditorNodes(document, expandedRoots, target.id, childCount)
  }
  const siblings = document.nodes
    .filter(
      (node) => node.parent_id === target.parent_id && !moving.has(node.id),
    )
    .sort(
      (left, right) =>
        left.order_key.localeCompare(right.order_key) ||
        left.id.localeCompare(right.id),
    )
  const targetIndex = siblings.findIndex((node) => node.id === target.id)
  if (targetIndex < 0) return document
  // The panel renders high order keys first. A visual drop before the row is
  // therefore inserted after the target in the ascending document order.
  const insertionIndex = position === 'before' ? targetIndex + 1 : targetIndex
  return reparentEditorNodes(
    document,
    expandedRoots,
    target.parent_id,
    insertionIndex,
  )
}

function expandAdjustmentCompanions(
  document: EditorDocumentV2,
  nodeIDs: readonly string[],
) {
  const selected = new Set(nodeIDs)
  for (const node of document.nodes) {
    if (
      node.type === 'adjustment' &&
      node.target_id &&
      selected.has(node.target_id)
    )
      selected.add(node.id)
  }
  return [...selected]
}
