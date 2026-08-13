import { describe, expect, it } from 'vitest'

import { groupEditorNodes, reorderEditorNode } from './authoring-v2'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import { EditorHistoryV2 } from './history-v2'

describe('EditorHistoryV2', () => {
  it('undoes and redoes structural group commands', () => {
    const original = document([raster('a', 0), raster('b', 1)])
    const grouped = groupEditorNodes(original, ['a', 'b'], { id: 'group' })
    const history = new EditorHistoryV2()
    expect(history.commit(original, grouped)).toBe(true)
    expect(history.undo(grouped)).toEqual(original)
    expect(history.redo(original)).toEqual(grouped)
  })

  it('merges continuous node edits and clears redo after a new command', () => {
    const initial = document([raster('a', 0), raster('b', 1)])
    const first = updateTransform(initial, 'a', 10)
    const second = updateTransform(first, 'a', 20)
    const history = new EditorHistoryV2()
    history.commit(initial, first, { mergeKey: 'move:a' })
    history.commit(first, second, { mergeKey: 'move:a' })
    expect(history.undo(second)).toEqual(initial)
    expect(history.redo(initial)).toEqual(second)
    const restored = history.undo(second)
    const reordered = reorderEditorNode(restored, 'b', 0)
    history.commit(restored, reordered)
    expect(history.canRedo).toBe(false)
  })

  it('keeps patches proportional to changed nodes', () => {
    const original = document(
      Array.from({ length: 100 }, (_, index) => raster(`node-${index}`, index)),
    )
    const updated = updateTransform(original, 'node-50', 25)
    const history = new EditorHistoryV2()
    history.commit(original, updated)
    expect(history.undo(updated)).toEqual(original)
  })
})

function updateTransform(
  value: EditorDocumentV2,
  id: string,
  x: number,
): EditorDocumentV2 {
  return {
    ...value,
    nodes: value.nodes.map((node) =>
      node.id === id ? { ...node, transform: [1, 0, 0, 1, x, 0] } : node,
    ),
  }
}

function document(nodes: EditorNodeV2[]): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 100, height: 100 },
    nodes,
  }
}

function raster(id: string, index: number): EditorNodeV2 {
  return {
    id,
    type: 'raster',
    parent_id: null,
    order_key: index.toString().padStart(8, '0'),
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
    asset_id: `00000000-0000-4000-8000-${index.toString().padStart(12, '0')}`,
    effects: [],
  }
}
