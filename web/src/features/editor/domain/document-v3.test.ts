import { describe, expect, it, vi } from 'vitest'

import type { EditorDocumentV2 } from './document-v2'
import {
  artboardAsDocumentV2,
  createBlankEditorArtboard,
  migrateEditorDocumentToV3,
  validateEditorDocumentV3,
} from './document-v3'

const source: EditorDocumentV2 = {
  schema_version: 2,
  renderer_semantics_version: 1,
  canvas: { width: 640, height: 480 },
  nodes: [
    {
      id: 'node-1',
      type: 'raster',
      parent_id: null,
      order_key: '000001',
      transform: [1, 0, 0, 1, 0, 0],
      opacity: 1,
      blend_mode: 'normal',
      visible: true,
      locked: false,
      asset_id: 'asset-1',
    },
  ],
}

describe('editor document V3', () => {
  it('migrates V2 without changing active artboard pixels', () => {
    const migrated = migrateEditorDocumentToV3(source)
    expect(migrated).toMatchObject({
      schema_version: 3,
      renderer_semantics_version: 2,
      active_artboard_id: 'artboard-1',
    })
    expect(artboardAsDocumentV2(migrated.artboards[0])).toEqual(source)
    expect(validateEditorDocumentV3(migrated)).toEqual([])
  })

  it('creates an independent blank artboard after the current bounds', () => {
    const randomUUID = vi.fn().mockReturnValueOnce('artboard-2')
    vi.stubGlobal('crypto', { randomUUID })
    const migrated = migrateEditorDocumentToV3(source)
    const blank = createBlankEditorArtboard(migrated, {
      width: 1920,
      height: 1080,
    })
    expect(blank).toMatchObject({
      id: 'artboard-2',
      x: 800,
      width: 1920,
      height: 1080,
      nodes: [],
    })
    expect(
      validateEditorDocumentV3({
        ...migrated,
        artboards: [...migrated.artboards, blank],
      }),
    ).toEqual([])
    vi.unstubAllGlobals()
  })
})
