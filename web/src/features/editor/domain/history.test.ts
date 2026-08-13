import { describe, expect, it } from 'vitest'

import { EditorHistory, diffEditorDocuments } from './history'
import type { EditorDocument, EditorObject } from './document'

const first: EditorObject = {
  id: 'first',
  name: 'First',
  asset_id: 'asset-1',
  transform: [1, 0, 0, 1, 0, 0],
  opacity: 1,
  visible: true,
  locked: false,
  z_index: 0,
}
const second: EditorObject = {
  ...first,
  id: 'second',
  name: 'Second',
  asset_id: 'asset-2',
  z_index: 1,
}

function document(objects = [first, second]): EditorDocument {
  return {
    schema_version: 1,
    canvas: { width: 2048, height: 1024 },
    objects,
  }
}

describe('EditorHistory', () => {
  it('stores only changed objects for a transform', () => {
    const before = document()
    const after = document([
      { ...first, transform: [1, 0, 0, 1, 120, 40] },
      second,
    ])
    const patch = diffEditorDocuments(before, after)
    expect(patch.objects).toHaveLength(1)
    expect(patch.objects[0].id).toBe('first')
    expect(patch.order).toBeUndefined()
    expect(patch.canvas).toBeUndefined()
  })

  it('undoes and redoes add, remove, reorder and canvas changes', () => {
    const before = document()
    const third = { ...first, id: 'third', asset_id: 'asset-3', z_index: 0 }
    const after: EditorDocument = {
      schema_version: 1,
      canvas: { width: 1024, height: 1024 },
      objects: [third, { ...second, z_index: 1 }],
    }
    const history = new EditorHistory()
    expect(history.commit(before, after)).toBe(true)
    expect(history.undo(after)).toEqual(before)
    expect(history.redo(before)).toEqual(after)
  })

  it('coalesces commands sharing a merge key', () => {
    const initial = document()
    const movedOnce = document([
      { ...first, transform: [1, 0, 0, 1, 1, 0] },
      second,
    ])
    const movedTwice = document([
      { ...first, transform: [1, 0, 0, 1, 2, 0] },
      second,
    ])
    const history = new EditorHistory()
    history.commit(initial, movedOnce, { mergeKey: 'nudge:first' })
    history.commit(movedOnce, movedTwice, { mergeKey: 'nudge:first' })
    expect(history.undo(movedTwice)).toEqual(initial)
    expect(history.canUndo).toBe(false)
  })

  it('clears redo history after a new command', () => {
    const initial = document()
    const moved = document([
      { ...first, transform: [1, 0, 0, 1, 4, 0] },
      second,
    ])
    const resized = { ...initial, canvas: { width: 1000, height: 1000 } }
    const history = new EditorHistory()
    history.commit(initial, moved)
    history.undo(moved)
    expect(history.canRedo).toBe(true)
    history.commit(initial, resized)
    expect(history.canRedo).toBe(false)
  })

  it('respects the configured history limit', () => {
    const history = new EditorHistory(2)
    const a = document()
    const b = document([{ ...first, opacity: 0.9 }, second])
    const c = document([{ ...first, opacity: 0.8 }, second])
    const d = document([{ ...first, opacity: 0.7 }, second])
    history.commit(a, b)
    history.commit(b, c)
    history.commit(c, d)
    expect(history.undo(d)).toEqual(c)
    expect(history.undo(c)).toEqual(b)
    expect(history.undo(b)).toEqual(b)
  })
})
