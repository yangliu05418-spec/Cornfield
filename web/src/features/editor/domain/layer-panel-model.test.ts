import { describe, expect, it } from 'vitest'

import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import {
  buildVisibleEditorLayerRows,
  canAttachEditorMask,
  canGroupEditorNodes,
  moveEditorNodesByDrop,
  reorderEditorNodeRelative,
} from './layer-panel-model'

const transform: EditorNodeV2['transform'] = [1, 0, 0, 1, 0, 0]

function raster(
  id: string,
  orderKey: string,
  parentID: string | null = null,
): EditorNodeV2 {
  return {
    id,
    type: 'raster',
    parent_id: parentID,
    order_key: orderKey,
    transform,
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
    asset_id: `asset-${id}`,
  }
}

function group(id: string, orderKey: string): EditorNodeV2 {
  return {
    ...raster(id, orderKey),
    type: 'group',
    asset_id: undefined,
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

describe('structured layer panel model', () => {
  it('orders high layers first while keeping descendants with their group', () => {
    const value = document([
      raster('bottom', '00000000'),
      group('group', '00000001'),
      raster('child-low', '00000000', 'group'),
      raster('child-high', '00000001', 'group'),
    ])
    expect(
      buildVisibleEditorLayerRows(value, new Set()).map(
        ({ entry }) => `${entry.depth}:${entry.node.id}`,
      ),
    ).toEqual(['0:group', '1:child-high', '1:child-low', '0:bottom'])
    expect(
      buildVisibleEditorLayerRows(value, new Set(['group'])).map(
        ({ entry }) => entry.node.id,
      ),
    ).toEqual(['group', 'bottom'])
  })

  it('only groups siblings and only masks a sibling raster', () => {
    const first = raster('first', '00000000')
    const second = raster('second', '00000001')
    const nested = raster('nested', '00000000', 'container')
    expect(canGroupEditorNodes([first, second])).toBe(true)
    expect(canGroupEditorNodes([first, nested])).toBe(false)
    expect(canAttachEditorMask([first, second], first)).toBe(true)
    expect(canAttachEditorMask([first, nested], first)).toBe(false)
  })

  it('moves within siblings without crossing the parent boundary', () => {
    const value = document([
      raster('first', '00000000'),
      raster('second', '00000001'),
      group('container', '00000002'),
      raster('nested', '00000000', 'container'),
    ])
    const moved = reorderEditorNodeRelative(value, 'first', 1)
    expect(
      moved.nodes
        .filter((node) => node.parent_id === null)
        .sort((a, b) => a.order_key.localeCompare(b.order_key))
        .map((node) => node.id),
    ).toEqual(['second', 'first', 'container'])
    expect(reorderEditorNodeRelative(moved, 'nested', 1)).toBe(moved)
  })

  it('maps visual drop positions to document order and group nesting', () => {
    const value = document([
      raster('bottom', '00000000'),
      raster('middle', '00000001'),
      group('container', '00000002'),
      raster('top', '00000003'),
    ])
    const above = moveEditorNodesByDrop(value, ['bottom'], 'middle', 'before')
    expect(
      above.nodes
        .filter((node) => node.parent_id === null)
        .sort((a, b) => a.order_key.localeCompare(b.order_key))
        .map((node) => node.id),
    ).toEqual(['middle', 'bottom', 'container', 'top'])
    const nested = moveEditorNodesByDrop(above, ['top'], 'container', 'inside')
    expect(nested.nodes.find((node) => node.id === 'top')?.parent_id).toBe(
      'container',
    )
    expect(moveEditorNodesByDrop(nested, ['top'], 'top', 'after')).toBe(nested)
  })

  it('moves only the selected root when its descendant is selected too', () => {
    const container = group('container', '00000001')
    const child = raster('child', '00000000', 'container')
    const target = group('target', '00000002')
    const moved = moveEditorNodesByDrop(
      document([container, child, target]),
      ['container', 'child'],
      'target',
      'inside',
    )
    expect(moved.nodes.find((node) => node.id === 'container')?.parent_id).toBe(
      'target',
    )
    expect(moved.nodes.find((node) => node.id === 'child')?.parent_id).toBe(
      'container',
    )
  })
})
