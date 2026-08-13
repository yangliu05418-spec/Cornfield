import type { EditorDocumentV1, EditorObject } from './document'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import {
  compileEditorDocumentV2ToV1,
  UnsupportedEditorSemanticsError,
  validateEditorDocumentV2,
} from './document-v2'

export function isFlatEditorDocumentV2(document: EditorDocumentV2) {
  return document.nodes.every(
    (node) =>
      node.type === 'raster' &&
      node.parent_id === null &&
      node.mask_id === undefined &&
      node.blend_mode === 'normal' &&
      (node.effects?.length ?? 0) === 0 &&
      node.asset_id !== undefined,
  )
}

export function projectFlatEditorDocumentV2(
  document: EditorDocumentV2,
): EditorDocumentV1 {
  if (!isFlatEditorDocumentV2(document))
    throw new UnsupportedEditorSemanticsError()
  return compileEditorDocumentV2ToV1(document)
}

export function applyFlatEditorViewToV2(
  document: EditorDocumentV2,
  view: EditorDocumentV1,
): EditorDocumentV2 {
  if (!isFlatEditorDocumentV2(document))
    throw new UnsupportedEditorSemanticsError()
  const existing = new Map(document.nodes.map((node) => [node.id, node]))
  const objects = [...view.objects].sort((left, right) => {
    return left.z_index - right.z_index || left.id.localeCompare(right.id)
  })
  const nodes = objects.map((object, index) =>
    mergeFlatObject(existing.get(object.id), object, index),
  )
  const next: EditorDocumentV2 = {
    ...document,
    canvas: { ...view.canvas },
    nodes,
  }
  if (validateEditorDocumentV2(next).length > 0)
    throw new TypeError('Flat editor update produced an invalid V2 document')
  return next
}

function mergeFlatObject(
  existing: EditorNodeV2 | undefined,
  object: EditorObject,
  index: number,
): EditorNodeV2 {
  return {
    ...(existing ?? {}),
    id: object.id,
    type: 'raster',
    name: object.name,
    parent_id: null,
    order_key: index.toString().padStart(8, '0'),
    transform: [...object.transform],
    opacity: object.opacity,
    blend_mode: 'normal',
    visible: object.visible,
    locked: object.locked,
    mask_id: undefined,
    asset_id: object.asset_id,
    crop: object.crop ? { ...object.crop } : undefined,
    effects: [],
  }
}
