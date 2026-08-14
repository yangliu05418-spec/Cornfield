import type { EditorDocumentV2, EditorShapeMaskV2 } from './document-v2'

export class EditorShapeMaskError extends Error {
  constructor(message = '当前图层无法使用形状蒙版') {
    super(message)
    this.name = 'EditorShapeMaskError'
  }
}

export function setEditorShapeMask(
  document: EditorDocumentV2,
  nodeID: string,
  mask: EditorShapeMaskV2,
): EditorDocumentV2 {
  const node = document.nodes.find((candidate) => candidate.id === nodeID)
  if (
    node?.type !== 'raster' ||
    node.mask_id !== undefined ||
    node.crop !== undefined ||
    document.nodes.some((candidate) => candidate.mask_id === nodeID)
  )
    throw new EditorShapeMaskError()
  if (!validMask(mask)) throw new EditorShapeMaskError('选区超出图层范围')
  return {
    ...document,
    nodes: document.nodes.map((candidate) =>
      candidate.id === nodeID
        ? { ...candidate, shape_mask: { ...mask } }
        : candidate,
    ),
  }
}

export function removeEditorShapeMask(
  document: EditorDocumentV2,
  nodeID: string,
): EditorDocumentV2 {
  return {
    ...document,
    nodes: document.nodes.map((node) => {
      if (node.id !== nodeID || node.shape_mask === undefined) return node
      const { shape_mask: _shapeMask, ...withoutMask } = node
      return withoutMask
    }),
  }
}

export function invertEditorShapeMask(
  document: EditorDocumentV2,
  nodeID: string,
): EditorDocumentV2 {
  const node = document.nodes.find((candidate) => candidate.id === nodeID)
  if (!node?.shape_mask) throw new EditorShapeMaskError('当前图层没有形状蒙版')
  return setEditorShapeMask(document, nodeID, {
    ...node.shape_mask,
    inverted: !node.shape_mask.inverted,
  })
}

function validMask(mask: EditorShapeMaskV2) {
  return (
    [mask.x, mask.y, mask.width, mask.height].every(Number.isFinite) &&
    mask.x >= 0 &&
    mask.y >= 0 &&
    mask.width > 0 &&
    mask.height > 0 &&
    mask.x + mask.width <= 1 &&
    mask.y + mask.height <= 1
  )
}
