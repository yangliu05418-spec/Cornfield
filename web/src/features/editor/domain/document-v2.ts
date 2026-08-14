import type {
  EditorCrop,
  EditorDocumentV1,
  EditorObject,
  EditorTransform,
} from './document'

export type EditorEffectV2 = {
  type: 'exposure' | 'contrast' | 'saturation' | 'temperature'
  version: 1
  enabled: boolean
  parameters: Record<string, number>
}

export type EditorShapeMaskV2 = {
  type: 'rectangle' | 'ellipse'
  x: number
  y: number
  width: number
  height: number
  inverted: boolean
}

export type EditorPixelMaskV2 = {
  resource_id: string
  version: number
}

export type EditorNodeV2 = {
  id: string
  type: 'raster' | 'group' | 'adjustment'
  name?: string
  parent_id: string | null
  order_key: string
  transform: EditorTransform
  opacity: number
  blend_mode:
    'normal' | 'multiply' | 'screen' | 'overlay' | 'darken' | 'lighten'
  visible: boolean
  locked: boolean
  mask_id?: string
  asset_id?: string
  crop?: EditorCrop
  effects?: EditorEffectV2[]
  target_id?: string
  shape_mask?: EditorShapeMaskV2
  pixel_mask?: EditorPixelMaskV2
}

export type EditorDocumentV2 = {
  schema_version: 2
  renderer_semantics_version: 1
  canvas: { width: number; height: number }
  nodes: EditorNodeV2[]
}

export class UnsupportedEditorSemanticsError extends Error {
  constructor() {
    super('The document uses V2 semantics that the V1 renderer cannot preserve')
    this.name = 'UnsupportedEditorSemanticsError'
  }
}

export function migrateEditorDocumentV1ToV2(
  document: EditorDocumentV1,
): EditorDocumentV2 {
  const objects = [...document.objects].sort((a, b) => a.z_index - b.z_index)
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { ...document.canvas },
    nodes: objects.map((object, index) => ({
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
      asset_id: object.asset_id,
      crop: object.crop ? { ...object.crop } : undefined,
      effects: [],
    })),
  }
}

export function compileEditorDocumentV2ToV1(
  document: EditorDocumentV2,
): EditorDocumentV1 {
  if (validateEditorDocumentV2(document).length > 0) {
    throw new TypeError('Invalid editor document V2')
  }
  if (
    document.nodes.some(
      (node) =>
        node.type !== 'raster' ||
        node.parent_id !== null ||
        node.mask_id !== undefined ||
        node.target_id !== undefined ||
        node.shape_mask !== undefined ||
        node.pixel_mask !== undefined ||
        node.blend_mode !== 'normal' ||
        (node.effects?.length ?? 0) > 0 ||
        !node.asset_id,
    )
  ) {
    throw new UnsupportedEditorSemanticsError()
  }
  const nodes = [...document.nodes].sort(
    (a, b) =>
      a.order_key.localeCompare(b.order_key) || a.id.localeCompare(b.id),
  )
  return {
    schema_version: 1,
    canvas: { ...document.canvas },
    objects: nodes.map<EditorObject>((node, index) => ({
      id: node.id,
      name: node.name,
      asset_id: node.asset_id!,
      transform: [...node.transform],
      opacity: node.opacity,
      visible: node.visible,
      locked: node.locked,
      z_index: index,
      crop: node.crop ? { ...node.crop } : undefined,
    })),
  }
}

export function validateEditorDocumentV2(document: EditorDocumentV2): string[] {
  const errors: string[] = []
  const pixels = document.canvas.width * document.canvas.height
  if (
    !Number.isInteger(document.canvas.width) ||
    !Number.isInteger(document.canvas.height) ||
    document.canvas.width < 1 ||
    document.canvas.height < 1 ||
    document.canvas.width > 8192 ||
    document.canvas.height > 8192 ||
    pixels > 36_000_000
  )
    errors.push('canvas')
  if (document.nodes.length > 500) errors.push('nodes')

  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  if (nodes.size !== document.nodes.length) errors.push('duplicate-id')
  const orderKeys = new Set<string>()
  for (const node of document.nodes) {
    if (
      !node.id ||
      node.id.length > 64 ||
      node.order_key.length < 1 ||
      node.order_key.length > 64 ||
      !/^[A-Za-z0-9]+$/.test(node.order_key)
    )
      errors.push(`identity:${node.id}`)
    const orderIdentity = `${node.parent_id === null ? '\u0000' : `\u0001${node.parent_id}`}\u0000${node.order_key}`
    if (orderKeys.has(orderIdentity)) errors.push(`order:${node.id}`)
    orderKeys.add(orderIdentity)
    if (
      !validTransform(node.transform) ||
      !Number.isFinite(node.opacity) ||
      node.opacity < 0 ||
      node.opacity > 1 ||
      ![
        'normal',
        'multiply',
        'screen',
        'overlay',
        'darken',
        'lighten',
      ].includes(node.blend_mode)
    )
      errors.push(`geometry:${node.id}`)
    if (node.type === 'raster') {
      if (
        !node.asset_id ||
        node.target_id !== undefined ||
        !validCrop(node.crop) ||
        !validShapeMask(node.shape_mask) ||
        !validPixelMask(node.pixel_mask) ||
        (node.shape_mask !== undefined &&
          (node.crop !== undefined ||
            node.mask_id !== undefined ||
            node.pixel_mask !== undefined)) ||
        (node.pixel_mask !== undefined && node.mask_id !== undefined) ||
        !validEffects(node.effects ?? [])
      )
        errors.push(`asset:${node.id}`)
    } else if (node.type === 'group') {
      if (
        node.asset_id !== undefined ||
        node.crop !== undefined ||
        node.target_id !== undefined ||
        node.shape_mask !== undefined ||
        node.pixel_mask !== undefined ||
        (node.effects?.length ?? 0) > 0
      )
        errors.push(`group:${node.id}`)
    } else if (
      node.asset_id !== undefined ||
      node.crop !== undefined ||
      node.mask_id !== undefined ||
      node.shape_mask !== undefined ||
      node.pixel_mask !== undefined ||
      node.target_id === undefined ||
      node.target_id === node.id ||
      node.blend_mode !== 'normal' ||
      node.transform.some(
        (value, index) => value !== [1, 0, 0, 1, 0, 0][index],
      ) ||
      !validEffects(node.effects ?? [])
    )
      errors.push(`adjustment:${node.id}`)
    if (node.parent_id !== null && nodes.get(node.parent_id)?.type !== 'group')
      errors.push(`parent:${node.id}`)
    if (
      node.mask_id !== undefined &&
      nodes.get(node.mask_id)?.type !== 'raster'
    )
      errors.push(`mask:${node.id}`)
    if (node.type === 'adjustment') {
      const target = nodes.get(node.target_id!)
      if (
        target?.type !== 'raster' ||
        target.parent_id !== node.parent_id ||
        document.nodes.some((candidate) => candidate.mask_id === target.id)
      )
        errors.push(`target:${node.id}`)
    }
    if (
      node.shape_mask !== undefined &&
      document.nodes.some((candidate) => candidate.mask_id === node.id)
    )
      errors.push(`shape-mask-source:${node.id}`)
    if (
      node.pixel_mask !== undefined &&
      document.nodes.some((candidate) => candidate.mask_id === node.id)
    )
      errors.push(`pixel-mask-source:${node.id}`)
    if (
      !validAncestry(node, nodes, 'parent_id', 32) ||
      !validAncestry(node, nodes, 'mask_id', 500)
    )
      errors.push(`cycle:${node.id}`)
  }
  return errors
}

function validCrop(crop?: EditorCrop) {
  if (!crop) return true
  return (
    [crop.x, crop.y, crop.width, crop.height].every(Number.isFinite) &&
    crop.x >= 0 &&
    crop.y >= 0 &&
    crop.width > 0 &&
    crop.height > 0 &&
    crop.x + crop.width <= 1 &&
    crop.y + crop.height <= 1
  )
}

function validShapeMask(mask?: EditorShapeMaskV2) {
  if (!mask) return true
  const runtimeType: string = mask.type
  return (
    (runtimeType === 'rectangle' || runtimeType === 'ellipse') &&
    [mask.x, mask.y, mask.width, mask.height].every(Number.isFinite) &&
    typeof mask.inverted === 'boolean' &&
    mask.x >= 0 &&
    mask.y >= 0 &&
    mask.width > 0 &&
    mask.height > 0 &&
    mask.x + mask.width <= 1 &&
    mask.y + mask.height <= 1
  )
}

function validPixelMask(mask?: EditorPixelMaskV2) {
  return (
    mask === undefined ||
    (/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(
      mask.resource_id,
    ) &&
      Number.isSafeInteger(mask.version) &&
      mask.version >= 0)
  )
}

function validEffects(effects: EditorEffectV2[]) {
  if (effects.length > 16) return false
  const definitions: Partial<
    Record<string, Partial<Record<string, [number, number]>>>
  > = {
    exposure: { stops: [-5, 5] },
    contrast: { amount: [-1, 1] },
    saturation: { amount: [-1, 1] },
    temperature: { kelvin_delta: [-10_000, 10_000] },
  }
  return effects.every((effect) => {
    const definition = definitions[effect.type]
    const entries = Object.entries(effect.parameters)
    return (
      definition !== undefined &&
      entries.length === Object.keys(definition).length &&
      entries.every(([key, value]) => {
        const bounds = definition[key]
        return (
          bounds !== undefined &&
          Number.isFinite(value) &&
          value >= bounds[0] &&
          value <= bounds[1]
        )
      })
    )
  })
}

function validTransform(transform: EditorTransform) {
  return (
    transform.every(
      (value) => Number.isFinite(value) && Math.abs(value) <= 1_000_000,
    ) &&
    Math.abs(transform[0] * transform[3] - transform[1] * transform[2]) >= 1e-8
  )
}

function validAncestry(
  start: EditorNodeV2,
  nodes: Map<string, EditorNodeV2>,
  key: 'parent_id' | 'mask_id',
  maximumDepth: number,
) {
  const seen = new Set([start.id])
  let depth = 1
  let nextID = start[key]
  while (nextID) {
    if (seen.has(nextID) || depth >= maximumDepth) return false
    seen.add(nextID)
    const current = nodes.get(nextID)
    if (!current) return false
    nextID = current[key]
    depth += 1
  }
  return true
}
