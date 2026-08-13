import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import type { EditorDocumentV1, EditorTransform } from '../domain/document'
import type { EditorDocumentV2 } from '../domain/document-v2'
import {
  compileEditorRenderScene,
  multiplyTransforms,
  UnsupportedEditorRenderSemanticsError,
} from './scene-compiler'

const identity: EditorTransform = [1, 0, 0, 1, 0, 0]

describe('editor scene compiler', () => {
  it('preserves V1 ordering and renderer semantics', () => {
    const document: EditorDocumentV1 = {
      schema_version: 1,
      canvas: { width: 100, height: 80 },
      objects: [
        {
          id: 'front',
          asset_id: 'asset-front',
          transform: [...identity],
          opacity: 0.5,
          visible: true,
          locked: false,
          z_index: 2,
        },
        {
          id: 'back',
          asset_id: 'asset-back',
          transform: [1, 0, 0, 1, 4, 5],
          opacity: 1,
          visible: true,
          locked: false,
          z_index: 0,
        },
      ],
    }
    expect(compileEditorRenderScene(document).nodes).toMatchObject([
      { id: 'back', order: 0, role: 'content' },
      { id: 'front', order: 1, role: 'content' },
    ])
  })

  it('accumulates nested group geometry, opacity and visibility deterministically', () => {
    const document = v2([
      group('group', null, '00000001', [2, 0, 0, 2, 10, 20], 0.5),
      raster('nested', 'group', '00000002', [1, 0, 0, 1, 3, 4], 0.4),
      raster('root', null, '00000002', [1, 0, 0, 1, 0, 0], 1),
    ])
    const scene = compileEditorRenderScene(document)
    expect(scene.nodes.map((node) => node.id)).toEqual(['nested', 'root'])
    expect(scene.nodes[0]).toMatchObject({
      transform: [2, 0, 0, 2, 16, 28],
      opacity: 0.2,
      order: 0,
    })
  })

  it('reserves raster mask nodes and keeps their independent world transform', () => {
    const target = raster('target', null, '00000002', identity, 1)
    target.mask_id = 'mask'
    const document = v2([
      raster('mask', null, '00000001', [1, 0, 0, 1, 20, 10], 1),
      target,
    ])
    expect(compileEditorRenderScene(document).nodes).toEqual([
      expect.objectContaining({
        id: 'mask',
        role: 'mask',
        transform: [1, 0, 0, 1, 20, 10],
      }),
      expect.objectContaining({
        id: 'target',
        role: 'content',
        maskNodeID: 'mask',
      }),
    ])
  })

  it('matches the shared V2 group and mask scene fixture', () => {
    const document = fixture('v2-group-mask.json') as EditorDocumentV2
    const scene = compileEditorRenderScene(document)
    expect(scene).toMatchObject({
      canvas: { width: 256, height: 256 },
      nodes: [
        expect.objectContaining({
          id: 'mask',
          assetID: '33333333-3333-4333-8333-333333333333',
          transform: [1, 0, 0, 1, 76, 66],
          opacity: 0.65,
          role: 'mask',
          order: 0,
        }),
        expect.objectContaining({
          id: 'content',
          assetID: '44444444-4444-4444-8444-444444444444',
          transform: [0, 1.2, -1.2, 0, 199.2, 57.6],
          role: 'content',
          maskNodeID: 'mask',
          order: 1,
        }),
      ],
    })
    expect(scene.nodes[1]?.opacity).toBeCloseTo(0.56)
  })

  it('rejects unimplemented semantics instead of flattening them', () => {
    const blend = raster('blend', null, '00000001', identity, 1)
    blend.blend_mode = 'multiply'
    expect(() => compileEditorRenderScene(v2([blend]))).toThrow(
      UnsupportedEditorRenderSemanticsError,
    )

    const chained = raster('target', null, '00000003', identity, 1)
    const mask = raster('mask', null, '00000002', identity, 1)
    const secondMask = raster('second-mask', null, '00000001', identity, 1)
    chained.mask_id = mask.id
    mask.mask_id = secondMask.id
    expect(() =>
      compileEditorRenderScene(v2([secondMask, mask, chained])),
    ).toThrow(UnsupportedEditorRenderSemanticsError)

    mask.mask_id = undefined
    mask.crop = { x: 0, y: 0, width: 0.5, height: 1 }
    expect(() =>
      compileEditorRenderScene(v2([secondMask, mask, chained])),
    ).toThrow(UnsupportedEditorRenderSemanticsError)
  })

  it('multiplies CSS-compatible affine matrices', () => {
    expect(
      multiplyTransforms([0, 1, -1, 0, 10, 20], [2, 0, 0, 3, 4, 5]),
    ).toEqual([0, 2, -3, 0, 5, 24])
  })
})

function v2(nodes: EditorDocumentV2['nodes']): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 100, height: 80 },
    nodes,
  }
}

function fixture(name: string) {
  return JSON.parse(
    readFileSync(
      new URL(`../../../../../testdata/editor/${name}`, import.meta.url),
      'utf8',
    ),
  )
}

function group(
  id: string,
  parentID: string | null,
  orderKey: string,
  transform: EditorDocumentV2['nodes'][number]['transform'],
  opacity: number,
): EditorDocumentV2['nodes'][number] {
  return {
    id,
    type: 'group',
    parent_id: parentID,
    order_key: orderKey,
    transform: [...transform],
    opacity,
    blend_mode: 'normal',
    visible: true,
    locked: false,
  }
}

function raster(
  id: string,
  parentID: string | null,
  orderKey: string,
  transform: EditorDocumentV2['nodes'][number]['transform'],
  opacity: number,
): EditorDocumentV2['nodes'][number] {
  return {
    id,
    type: 'raster',
    parent_id: parentID,
    order_key: orderKey,
    transform: [...transform],
    opacity,
    blend_mode: 'normal',
    visible: true,
    locked: false,
    asset_id: `00000000-0000-4000-8000-${id.padEnd(12, '0').slice(0, 12)}`,
    effects: [],
  }
}
