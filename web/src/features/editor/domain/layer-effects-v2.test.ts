import { describe, expect, it } from 'vitest'

import type { EditorDocumentV2 } from './document-v2'
import {
  EditorLayerEffectError,
  createEditorAdjustmentLayer,
  setEditorLayerBlendMode,
  setEditorLayerEffectEnabled,
  setEditorLayerEffectValue,
} from './layer-effects-v2'

describe('V2 non-destructive layer effects', () => {
  it('enables, edits and disables an ordered effect without changing pixels', () => {
    const enabled = setEditorLayerEffectEnabled(
      document(),
      'content',
      'exposure',
      true,
    )
    expect(enabled.nodes[0].effects).toEqual([
      { type: 'exposure', version: 1, enabled: true, parameters: { stops: 0 } },
    ])
    const edited = setEditorLayerEffectValue(
      enabled,
      'content',
      'exposure',
      1.5,
    )
    const disabled = setEditorLayerEffectEnabled(
      edited,
      'content',
      'exposure',
      false,
    )
    expect(disabled.nodes[0].effects?.[0]).toMatchObject({
      enabled: false,
      parameters: { stops: 1.5 },
    })
  })

  it('sets supported blend modes on ordinary raster layers', () => {
    expect(
      setEditorLayerBlendMode(document(), 'content', 'screen').nodes[0]
        .blend_mode,
    ).toBe('screen')
  })

  it('rejects group and alpha-mask adjustments', () => {
    const value = document()
    value.nodes.push({
      id: 'target',
      type: 'raster',
      parent_id: null,
      order_key: '00000002',
      transform: [1, 0, 0, 1, 0, 0],
      opacity: 1,
      blend_mode: 'normal',
      visible: true,
      locked: false,
      asset_id: 'asset-2',
      mask_id: 'content',
    })
    expect(() =>
      setEditorLayerEffectEnabled(value, 'content', 'contrast', true),
    ).toThrow(EditorLayerEffectError)
  })

  it('creates a non-pixel adjustment immediately above its target', () => {
    const value = createEditorAdjustmentLayer(document(), 'content', {
      id: 'adjustment',
    })
    expect(value.nodes.find((node) => node.id === 'adjustment')).toMatchObject({
      type: 'adjustment',
      target_id: 'content',
      transform: [1, 0, 0, 1, 0, 0],
      blend_mode: 'normal',
      effects: [],
    })
    expect(
      [...value.nodes]
        .sort((a, b) => a.order_key.localeCompare(b.order_key))
        .map((node) => node.id),
    ).toEqual(['content', 'adjustment'])
  })
})

function document(): EditorDocumentV2 {
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
        effects: [],
      },
    ],
  }
}
