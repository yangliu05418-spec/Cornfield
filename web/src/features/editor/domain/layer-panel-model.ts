import { buildEditorLayerTree, reorderEditorNode } from './authoring-v2'
import type { EditorLayerTreeNode } from './authoring-v2'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'

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
  return (
    nodes.length > 0 &&
    nodes.every((node) => node.parent_id === nodes[0]?.parent_id)
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
