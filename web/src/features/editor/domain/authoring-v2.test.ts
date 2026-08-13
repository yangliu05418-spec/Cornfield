import { describe, expect, it } from 'vitest'

import { compileEditorRenderScene } from '../renderer/scene-compiler'
import {
  attachEditorMask,
  buildEditorLayerTree,
  detachEditorMask,
  EditorCommandError,
  groupEditorNodes,
  reparentEditorNodes,
  reorderEditorNode,
  ungroupEditorNode,
} from './authoring-v2'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import { validateEditorDocumentV2 } from './document-v2'

describe('editor V2 authoring commands', () => {
  it('groups sibling nodes in place and exposes a stable tree', () => {
    const document = v2([raster('back', null, 0), raster('front', null, 1)])
    const grouped = groupEditorNodes(document, ['front'], {
      id: 'group',
      name: '主体',
    })
    expect(validateEditorDocumentV2(grouped)).toEqual([])
    expect(buildEditorLayerTree(grouped)).toMatchObject([
      { node: { id: 'back' }, depth: 0, children: [] },
      {
        node: { id: 'group', name: '主体' },
        depth: 0,
        children: [{ node: { id: 'front' }, depth: 1 }],
      },
    ])
  })

  it('ungroups without changing render transforms or inherited appearance', () => {
    const child = raster('child', 'group', 0)
    child.transform = [1, 0, 0, 1, 3, 4]
    child.opacity = 0.8
    const group = groupNode('group', null, 0)
    group.transform = [0, 1.2, -1.2, 0, 204, 54]
    group.opacity = 0.7
    const document = v2([group, child])
    const before = compileEditorRenderScene(document)
    const ungrouped = ungroupEditorNode(document, 'group')
    expect(compileEditorRenderScene(ungrouped)).toEqual(before)
    expect(ungrouped.nodes).toHaveLength(1)
  })

  it('attaches and detaches an independent raster alpha mask', () => {
    const document = v2([raster('mask', null, 0), raster('content', null, 1)])
    const attached = attachEditorMask(document, 'content', 'mask')
    expect(attached.nodes.find((node) => node.id === 'content')?.mask_id).toBe(
      'mask',
    )
    expect(compileEditorRenderScene(attached).nodes).toMatchObject([
      { id: 'mask', role: 'mask' },
      { id: 'content', role: 'content', maskNodeID: 'mask' },
    ])
    const detached = detachEditorMask(attached, 'content')
    expect(
      detached.nodes.find((node) => node.id === 'content')?.mask_id,
    ).toBeUndefined()
  })

  it('rejects mask reuse and separating a content/mask pair', () => {
    const document = attachEditorMask(
      v2([
        raster('mask', null, 0),
        raster('content', null, 1),
        raster('other', null, 2),
        groupNode('group', null, 3),
      ]),
      'content',
      'mask',
    )
    expect(() => attachEditorMask(document, 'other', 'mask')).toThrow(
      EditorCommandError,
    )
    expect(() => reparentEditorNodes(document, ['content'], 'group')).toThrow(
      EditorCommandError,
    )
  })

  it('reparents a subtree while preserving its world transform', () => {
    const left = groupNode('left', null, 0)
    left.transform = [2, 0, 0, 2, 10, 20]
    const right = groupNode('right', null, 1)
    right.transform = [1, 0, 0, 1, 100, 50]
    right.opacity = 0.5
    const subject = raster('subject', 'left', 0)
    subject.transform = [1, 0, 0, 1, 4, 5]
    subject.opacity = 0.25
    const document = v2([left, subject, right])
    const before = compileEditorRenderScene(document).nodes.find(
      (node) => node.id === 'subject',
    )
    const moved = reparentEditorNodes(document, ['subject'], 'right')
    const after = compileEditorRenderScene(moved).nodes.find(
      (node) => node.id === 'subject',
    )
    expect(after?.transform).toEqual(before?.transform)
    expect(after?.opacity).toBeCloseTo(before?.opacity ?? 0)
    expect(moved.nodes.find((node) => node.id === 'subject')?.parent_id).toBe(
      'right',
    )
  })

  it('rejects hierarchy cycles and reorders only within a sibling list', () => {
    const document = v2([
      groupNode('outer', null, 0),
      groupNode('inner', 'outer', 0),
      raster('a', null, 1),
      raster('b', null, 2),
    ])
    expect(() => reparentEditorNodes(document, ['outer'], 'inner')).toThrow(
      EditorCommandError,
    )
    const reordered = reorderEditorNode(document, 'b', 0)
    expect(
      buildEditorLayerTree(reordered).map((entry) => entry.node.id),
    ).toEqual(['b', 'outer', 'a'])
  })

  it('rejects moves that cannot preserve inherited appearance', () => {
    const source = groupNode('source', null, 0)
    source.opacity = 0.5
    const destination = groupNode('destination', null, 1)
    destination.opacity = 0.25
    const subject = raster('subject', 'source', 0)
    subject.opacity = 1
    const nested = groupNode('nested', 'source', 1)
    const child = raster('child', 'nested', 0)
    const document = v2([source, subject, nested, child, destination])
    expect(() =>
      reparentEditorNodes(document, ['subject'], 'destination'),
    ).toThrow(EditorCommandError)
    expect(() =>
      reparentEditorNodes(document, ['nested', 'child'], null),
    ).toThrow(EditorCommandError)
  })
})

function v2(nodes: EditorNodeV2[]): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 256, height: 256 },
    nodes,
  }
}

function raster(
  id: string,
  parentID: string | null,
  order: number,
): EditorNodeV2 {
  return {
    id,
    type: 'raster',
    parent_id: parentID,
    order_key: orderKey(order),
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
    asset_id: `00000000-0000-4000-8000-${id.padEnd(12, '0').slice(0, 12)}`,
    effects: [],
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
    order_key: orderKey(order),
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
  }
}

function orderKey(order: number) {
  return order.toString().padStart(8, '0')
}
