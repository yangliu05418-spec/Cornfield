import type {
  EditorCrop,
  EditorDocumentV1,
  EditorTransform,
} from '../domain/document'
import type {
  EditorDocumentV2,
  EditorEffectV2,
  EditorNodeV2,
} from '../domain/document-v2'
import { validateEditorDocumentV2 } from '../domain/document-v2'

export type EditorRenderDocument = EditorDocumentV1 | EditorDocumentV2

export type EditorSceneRasterNode = {
  id: string
  assetID: string
  transform: EditorTransform
  opacity: number
  visible: boolean
  order: number
  crop?: EditorCrop
  role: 'content' | 'mask'
  maskNodeID?: string
  blendMode: EditorNodeV2['blend_mode']
  effects: EditorEffectV2[]
}

export type EditorRenderScene = {
  canvas: { width: number; height: number }
  nodes: EditorSceneRasterNode[]
}

export class UnsupportedEditorRenderSemanticsError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'UnsupportedEditorRenderSemanticsError'
  }
}

export function compileEditorRenderScene(
  document: EditorRenderDocument,
): EditorRenderScene {
  if (document.schema_version === 1) return compileV1(document)
  return compileV2(document)
}

function compileV1(document: EditorDocumentV1): EditorRenderScene {
  return {
    canvas: { ...document.canvas },
    nodes: [...document.objects]
      .sort((left, right) => left.z_index - right.z_index)
      .map((object, order) => ({
        id: object.id,
        assetID: object.asset_id,
        transform: [...object.transform],
        opacity: object.opacity,
        visible: object.visible,
        order,
        crop: object.crop ? { ...object.crop } : undefined,
        role: 'content',
        blendMode: 'normal',
        effects: [],
      })),
  }
}

function compileV2(document: EditorDocumentV2): EditorRenderScene {
  if (validateEditorDocumentV2(document).length > 0)
    throw new TypeError('Invalid editor document V2')
  for (const node of document.nodes) {
    if (node.type === 'group' && node.blend_mode !== 'normal')
      throw new UnsupportedEditorRenderSemanticsError(
        `Group blend mode ${node.blend_mode} is not renderable yet`,
      )
    if (node.type === 'group' && node.mask_id !== undefined)
      throw new UnsupportedEditorRenderSemanticsError(
        'Group masks are not renderable yet',
      )
  }

  const byID = new Map(document.nodes.map((node) => [node.id, node]))
  const children = new Map<string | null, EditorNodeV2[]>()
  for (const node of document.nodes) {
    const siblings = children.get(node.parent_id) ?? []
    siblings.push(node)
    children.set(node.parent_id, siblings)
  }
  for (const siblings of children.values()) siblings.sort(compareNodes)

  const maskIDs = new Set(
    document.nodes.flatMap((node) => (node.mask_id ? [node.mask_id] : [])),
  )
  for (const maskID of maskIDs) {
    const mask = byID.get(maskID)!
    if (
      mask.mask_id !== undefined ||
      mask.crop !== undefined ||
      mask.blend_mode !== 'normal' ||
      (mask.effects ?? []).some((effect) => effect.enabled)
    )
      throw new UnsupportedEditorRenderSemanticsError(
        'Chained or cropped masks are not renderable yet',
      )
  }

  const compiled = new Map<string, EditorSceneRasterNode>()
  let order = 0
  const visit = (
    parentID: string | null,
    parentTransform: EditorTransform,
    parentOpacity: number,
    parentVisible: boolean,
  ) => {
    for (const node of children.get(parentID) ?? []) {
      const transform = multiplyTransforms(parentTransform, node.transform)
      const opacity = parentOpacity * node.opacity
      const visible = parentVisible && node.visible
      if (node.type === 'group') {
        visit(node.id, transform, opacity, visible)
        continue
      }
      compiled.set(node.id, {
        id: node.id,
        assetID: node.asset_id!,
        transform,
        opacity,
        visible,
        order,
        crop: node.crop ? { ...node.crop } : undefined,
        role: maskIDs.has(node.id) ? 'mask' : 'content',
        maskNodeID: node.mask_id,
        blendMode: node.blend_mode,
        effects: (node.effects ?? []).map((effect) => ({
          ...effect,
          parameters: { ...effect.parameters },
        })),
      })
      order += 1
    }
  }
  visit(null, [1, 0, 0, 1, 0, 0], 1, true)

  return {
    canvas: { ...document.canvas },
    nodes: [...compiled.values()],
  }
}

function compareNodes(left: EditorNodeV2, right: EditorNodeV2) {
  return (
    left.order_key.localeCompare(right.order_key) ||
    left.id.localeCompare(right.id)
  )
}

export function multiplyTransforms(
  parent: EditorTransform,
  child: EditorTransform,
): EditorTransform {
  return [
    parent[0] * child[0] + parent[2] * child[1],
    parent[1] * child[0] + parent[3] * child[1],
    parent[0] * child[2] + parent[2] * child[3],
    parent[1] * child[2] + parent[3] * child[3],
    parent[0] * child[4] + parent[2] * child[5] + parent[4],
    parent[1] * child[4] + parent[3] * child[5] + parent[5],
  ]
}
