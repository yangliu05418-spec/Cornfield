import { describe, expect, it } from 'vitest'

import type { EditorDocumentV1 } from './document'
import {
  applyFlatEditorViewToV2,
  isFlatEditorDocumentV2,
  projectFlatEditorDocumentV2,
} from './flat-authoring-v2'
import {
  migrateEditorDocumentV1ToV2,
  UnsupportedEditorSemanticsError,
} from './document-v2'

const original: EditorDocumentV1 = {
  schema_version: 1,
  canvas: { width: 100, height: 80 },
  objects: [
    {
      id: 'a',
      asset_id: '00000000-0000-4000-8000-000000000001',
      transform: [1, 0, 0, 1, 0, 0],
      opacity: 1,
      visible: true,
      locked: false,
      z_index: 0,
    },
    {
      id: 'b',
      asset_id: '00000000-0000-4000-8000-000000000002',
      transform: [1, 0, 0, 1, 10, 20],
      opacity: 0.8,
      visible: true,
      locked: false,
      z_index: 1,
    },
  ],
}

describe('flat V2 authoring adapter', () => {
  it('projects and applies flat edits without leaving V2', () => {
    const v2 = migrateEditorDocumentV1ToV2(original)
    const view = projectFlatEditorDocumentV2(v2)
    const edited: EditorDocumentV1 = {
      ...view,
      canvas: { width: 120, height: 90 },
      objects: [
        { ...view.objects[1], transform: [1, 0, 0, 1, 30, 40], z_index: 0 },
        {
          ...view.objects[0],
          opacity: 0.5,
          z_index: 1,
          crop: { x: 0, y: 0, width: 0.5, height: 1 },
        },
      ],
    }
    const result = applyFlatEditorViewToV2(v2, edited)
    expect(result.schema_version).toBe(2)
    expect(projectFlatEditorDocumentV2(result)).toEqual(edited)
  })

  it('supports adding and deleting flat raster nodes', () => {
    const v2 = migrateEditorDocumentV1ToV2(original)
    const view: EditorDocumentV1 = {
      ...original,
      objects: [
        original.objects[1],
        {
          ...original.objects[0],
          id: 'c',
          asset_id: '00000000-0000-4000-8000-000000000003',
          z_index: 1,
        },
      ],
    }
    expect(
      projectFlatEditorDocumentV2(
        applyFlatEditorViewToV2(v2, view),
      ).objects.map((object) => object.id),
    ).toEqual(['b', 'c'])
  })

  it('rejects group and mask documents rather than flattening them', () => {
    const grouped = migrateEditorDocumentV1ToV2(original)
    grouped.nodes.push({
      id: 'group',
      type: 'group',
      parent_id: null,
      order_key: '00000002',
      transform: [1, 0, 0, 1, 0, 0],
      opacity: 1,
      blend_mode: 'normal',
      visible: true,
      locked: false,
    })
    grouped.nodes[0].parent_id = 'group'
    expect(isFlatEditorDocumentV2(grouped)).toBe(false)
    expect(() => projectFlatEditorDocumentV2(grouped)).toThrow(
      UnsupportedEditorSemanticsError,
    )
  })
})
