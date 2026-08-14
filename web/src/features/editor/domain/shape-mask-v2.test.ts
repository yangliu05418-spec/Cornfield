import { describe, expect, it } from 'vitest'

import type { EditorDocumentV2 } from './document-v2'
import {
  invertEditorShapeMask,
  removeEditorShapeMask,
  setEditorShapeMask,
} from './shape-mask-v2'

describe('V2 shape masks', () => {
  it('sets, inverts and removes a normalized non-destructive mask', () => {
    const masked = setEditorShapeMask(makeDocument(), 'content', {
      type: 'rectangle',
      x: 0.1,
      y: 0.2,
      width: 0.6,
      height: 0.5,
      inverted: false,
    })
    expect(masked.nodes[0].shape_mask).toMatchObject({ inverted: false })
    expect(
      invertEditorShapeMask(masked, 'content').nodes[0].shape_mask,
    ).toMatchObject({ inverted: true })
    expect(
      removeEditorShapeMask(masked, 'content').nodes[0].shape_mask,
    ).toBeUndefined()
  })

  it('rejects mask stacking until the renderer supports isolated mask containers', () => {
    const value = makeDocument()
    value.nodes[0].crop = { x: 0, y: 0, width: 0.5, height: 1 }
    expect(() =>
      setEditorShapeMask(value, 'content', {
        type: 'ellipse',
        x: 0,
        y: 0,
        width: 1,
        height: 1,
        inverted: false,
      }),
    ).toThrow('无法使用形状蒙版')
  })
})

function makeDocument(): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 100, height: 100 },
    nodes: [
      {
        id: 'content',
        type: 'raster',
        parent_id: null,
        order_key: '00000001',
        transform: [1, 0, 0, 1, 0, 0],
        opacity: 1,
        blend_mode: 'normal',
        visible: true,
        locked: false,
        asset_id: 'asset-1',
      },
    ],
  }
}
