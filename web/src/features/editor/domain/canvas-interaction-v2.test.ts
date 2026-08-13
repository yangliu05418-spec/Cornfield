import { describe, expect, it } from 'vitest'

import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import {
  editorSelectionContainsNode,
  editorSelectionBounds,
  hitTestEditorDocument,
  translateEditorNodes,
} from './canvas-interaction-v2'

const assets = new Map([
  ['asset-a', { width: 100, height: 80 }],
  ['asset-b', { width: 40, height: 40 }],
])

describe('V2 canvas interaction geometry', () => {
  it('hits the topmost visible cropped raster', () => {
    const back = raster('back', null, 'asset-a', 0)
    const front = raster('front', null, 'asset-b', 1)
    front.transform = [1, 0, 0, 1, 20, 20]
    front.crop = { x: 0.5, y: 0, width: 0.5, height: 1 }
    const document = v2([back, front])
    expect(hitTestEditorDocument(document, assets, { x: 45, y: 30 })).toBe(
      'front',
    )
    expect(hitTestEditorDocument(document, assets, { x: 25, y: 30 })).toBe(
      'back',
    )
  })

  it('uses nested world transforms for group selection bounds', () => {
    const group = groupNode('group', null, 0)
    group.transform = [2, 0, 0, 2, 10, 20]
    const child = raster('child', 'group', 'asset-b', 0)
    child.transform = [1, 0, 0, 1, 5, 6]
    expect(
      editorSelectionBounds(v2([group, child]), assets, new Set(['group'])),
    ).toMatchObject({ left: 20, top: 32, right: 100, bottom: 112 })
  })

  it('translates selected roots in parent-local coordinates', () => {
    const group = groupNode('group', null, 0)
    group.transform = [2, 0, 0, 2, 10, 20]
    const child = raster('child', 'group', 'asset-b', 0)
    child.transform = [1, 0, 0, 1, 5, 6]
    const moved = translateEditorNodes(v2([group, child]), new Set(['child']), {
      x: 20,
      y: -10,
    })
    expect(moved.nodes.find((node) => node.id === 'child')?.transform).toEqual([
      1, 0, 0, 1, 15, 1,
    ])
  })

  it('does not double-move descendants selected with their parent', () => {
    const group = groupNode('group', null, 0)
    const child = raster('child', 'group', 'asset-b', 0)
    const moved = translateEditorNodes(
      v2([group, child]),
      new Set(['group', 'child']),
      { x: 8, y: 9 },
    )
    expect(moved.nodes.find((node) => node.id === 'group')?.transform).toEqual([
      1, 0, 0, 1, 8, 9,
    ])
    expect(moved.nodes.find((node) => node.id === 'child')?.transform).toEqual([
      1, 0, 0, 1, 0, 0,
    ])
  })

  it('keeps group selection when a descendant raster is hit', () => {
    const group = groupNode('group', null, 0)
    const child = raster('child', 'group', 'asset-b', 0)
    const document = v2([group, child])
    expect(
      editorSelectionContainsNode(document, new Set(['group']), 'child'),
    ).toBe(true)
    expect(
      editorSelectionContainsNode(document, new Set(['child']), 'group'),
    ).toBe(false)
  })

  it('clips hit testing and selection bounds to a raster mask', () => {
    const mask = raster('mask', null, 'asset-b', 0)
    mask.transform = [1, 0, 0, 1, 20, 10]
    const content = raster('content', null, 'asset-a', 1)
    content.mask_id = mask.id
    const document = v2([mask, content])
    expect(hitTestEditorDocument(document, assets, { x: 10, y: 20 })).toBe(
      undefined,
    )
    expect(hitTestEditorDocument(document, assets, { x: 30, y: 20 })).toBe(
      'content',
    )
    expect(
      editorSelectionBounds(document, assets, new Set(['content', 'mask'])),
    ).toMatchObject({ left: 20, top: 10, right: 60, bottom: 50 })
  })

  it('rejects moving a child of a locked group', () => {
    const group = groupNode('group', null, 0)
    group.locked = true
    const child = raster('child', 'group', 'asset-b', 0)
    expect(() =>
      translateEditorNodes(v2([group, child]), new Set(['child']), {
        x: 1,
        y: 1,
      }),
    ).toThrow('Locked')
  })
})

function v2(nodes: EditorNodeV2[]): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 300, height: 240 },
    nodes,
  }
}

function raster(
  id: string,
  parentID: string | null,
  assetID: string,
  order: number,
): EditorNodeV2 {
  return {
    id,
    type: 'raster',
    parent_id: parentID,
    order_key: order.toString().padStart(8, '0'),
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
    asset_id: assetID,
  }
}

function groupNode(
  id: string,
  parentID: string | null,
  order: number,
): EditorNodeV2 {
  return {
    id,
    type: 'group',
    parent_id: parentID,
    order_key: order.toString().padStart(8, '0'),
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
  }
}
