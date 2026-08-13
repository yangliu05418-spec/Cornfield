import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { EditorDocumentV1 } from './document'
import type { EditorDocumentV2 } from './document-v2'
import {
  compileEditorDocumentV2ToV1,
  migrateEditorDocumentV1ToV2,
  UnsupportedEditorSemanticsError,
  validateEditorDocumentV2,
} from './document-v2'

const fixture = (name: string) =>
  JSON.parse(
    readFileSync(
      new URL(`../../../../../testdata/editor/${name}`, import.meta.url),
      'utf8',
    ),
  )

describe('editor document V2', () => {
  it('matches the shared V1 migration golden and round trips the renderable subset', () => {
    const v1 = fixture('v1-flat.json') as EditorDocumentV1
    const expected = fixture('v2-flat.json') as EditorDocumentV2
    const v2 = migrateEditorDocumentV1ToV2(v1)
    expect(v2).toEqual(expected)
    expect(compileEditorDocumentV2ToV1(v2)).toEqual({
      ...v1,
      objects: [...v1.objects]
        .sort((a, b) => a.z_index - b.z_index)
        .map((object, index) => ({ ...object, z_index: index })),
    })
  })

  it('validates professional tree semantics without silently flattening them', () => {
    const v2 = fixture('v2-flat.json') as EditorDocumentV2
    v2.nodes.push({
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
    v2.nodes[0].parent_id = 'group'
    expect(validateEditorDocumentV2(v2)).toEqual([])
    expect(() => compileEditorDocumentV2ToV1(v2)).toThrow(
      UnsupportedEditorSemanticsError,
    )
  })

  it('validates an explicit clipped adjustment target', () => {
    const v2 = fixture('v2-flat.json') as EditorDocumentV2
    const target = v2.nodes[0]
    v2.nodes.push({
      id: 'adjustment',
      type: 'adjustment',
      target_id: target.id,
      parent_id: target.parent_id,
      order_key: '00000003',
      transform: [1, 0, 0, 1, 0, 0],
      opacity: 0.5,
      blend_mode: 'normal',
      visible: true,
      locked: false,
      effects: [],
    })
    expect(validateEditorDocumentV2(v2)).toEqual([])

    v2.nodes.at(-1)!.target_id = 'missing'
    expect(validateEditorDocumentV2(v2)).toContain('target:adjustment')
  })
})
