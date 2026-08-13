import type {
  EditorDocumentV2,
  EditorEffectV2,
  EditorNodeV2,
} from './document-v2'
import { normalizeEditorOrderKeys } from './authoring-v2'

export type EditorBlendModeV2 = EditorNodeV2['blend_mode']
export type EditorEffectTypeV2 = EditorEffectV2['type']

export const editorBlendModesV2: ReadonlyArray<{
  value: EditorBlendModeV2
  label: string
}> = [
  { value: 'normal', label: '正常' },
  { value: 'multiply', label: '正片叠底' },
  { value: 'screen', label: '滤色' },
  { value: 'overlay', label: '叠加' },
  { value: 'darken', label: '变暗' },
  { value: 'lighten', label: '变亮' },
]

export const editorEffectDefinitionsV2: ReadonlyArray<{
  type: EditorEffectTypeV2
  label: string
  parameter: string
  minimum: number
  maximum: number
  step: number
  defaultValue: number
  format: (value: number) => string
}> = [
  {
    type: 'exposure',
    label: '曝光',
    parameter: 'stops',
    minimum: -5,
    maximum: 5,
    step: 0.1,
    defaultValue: 0,
    format: (value) => `${value >= 0 ? '+' : ''}${value.toFixed(1)} EV`,
  },
  {
    type: 'contrast',
    label: '对比度',
    parameter: 'amount',
    minimum: -1,
    maximum: 1,
    step: 0.01,
    defaultValue: 0,
    format: formatPercent,
  },
  {
    type: 'saturation',
    label: '饱和度',
    parameter: 'amount',
    minimum: -1,
    maximum: 1,
    step: 0.01,
    defaultValue: 0,
    format: formatPercent,
  },
  {
    type: 'temperature',
    label: '色温',
    parameter: 'kelvin_delta',
    minimum: -10_000,
    maximum: 10_000,
    step: 100,
    defaultValue: 0,
    format: (value) => `${value >= 0 ? '+' : ''}${Math.round(value)} K`,
  },
]

export class EditorLayerEffectError extends Error {
  constructor(message = '当前图层不支持这项调整') {
    super(message)
    this.name = 'EditorLayerEffectError'
  }
}

export function setEditorLayerBlendMode(
  document: EditorDocumentV2,
  nodeID: string,
  blendMode: EditorBlendModeV2,
) {
  const node = document.nodes.find((candidate) => candidate.id === nodeID)
  if (node?.type !== 'raster')
    throw new EditorLayerEffectError('只有像素图层支持混合模式')
  return updateEffectNode(document, nodeID, (value) => ({
    ...value,
    blend_mode: blendMode,
  }))
}

export function setEditorLayerEffectEnabled(
  document: EditorDocumentV2,
  nodeID: string,
  type: EditorEffectTypeV2,
  enabled: boolean,
) {
  const definition = effectDefinition(type)
  return updateEffectNode(document, nodeID, (node) => {
    const effects = [...(node.effects ?? [])]
    const index = effects.findIndex((effect) => effect.type === type)
    if (index < 0) {
      effects.push({
        type,
        version: 1,
        enabled,
        parameters: { [definition.parameter]: definition.defaultValue },
      })
    } else {
      effects[index] = { ...effects[index], enabled }
    }
    return { ...node, effects }
  })
}

export function setEditorLayerEffectValue(
  document: EditorDocumentV2,
  nodeID: string,
  type: EditorEffectTypeV2,
  value: number,
) {
  const definition = effectDefinition(type)
  if (
    !Number.isFinite(value) ||
    value < definition.minimum ||
    value > definition.maximum
  )
    throw new EditorLayerEffectError('调整值超出有效范围')
  return updateEffectNode(document, nodeID, (node) => {
    const effects = [...(node.effects ?? [])]
    const index = effects.findIndex((effect) => effect.type === type)
    if (index < 0) throw new EditorLayerEffectError('请先启用这项调整')
    effects[index] = {
      ...effects[index],
      parameters: { [definition.parameter]: value },
    }
    return { ...node, effects }
  })
}

export function editorLayerSupportsEffects(
  document: EditorDocumentV2,
  nodeID: string,
) {
  const node = document.nodes.find((candidate) => candidate.id === nodeID)
  return (
    node?.type === 'adjustment' ||
    (node?.type === 'raster' &&
      !document.nodes.some((candidate) => candidate.mask_id === nodeID))
  )
}

export function createEditorAdjustmentLayer(
  document: EditorDocumentV2,
  targetID: string,
  options: { id: string; name?: string },
) {
  const target = document.nodes.find((node) => node.id === targetID)
  if (
    !target ||
    target.type !== 'raster' ||
    document.nodes.some((node) => node.id === options.id) ||
    document.nodes.some((node) => node.mask_id === target.id)
  )
    throw new EditorLayerEffectError('请选择一个普通像素图层')
  const siblings = document.nodes
    .filter((node) => node.parent_id === target.parent_id)
    .sort(
      (left, right) =>
        left.order_key.localeCompare(right.order_key) ||
        left.id.localeCompare(right.id),
    )
  const targetIndex = siblings.findIndex((node) => node.id === target.id)
  const adjustment: EditorNodeV2 = {
    id: options.id,
    type: 'adjustment',
    name: options.name ?? '调整层',
    parent_id: target.parent_id,
    order_key: '0',
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    blend_mode: 'normal',
    visible: true,
    locked: false,
    target_id: target.id,
    effects: [],
  }
  const next: EditorDocumentV2 = {
    ...document,
    nodes: [...document.nodes, adjustment],
  }
  siblings.splice(targetIndex + 1, 0, adjustment)
  return normalizeEditorOrderKeys(next, {
    parentID: target.parent_id,
    preferredOrder: siblings.map((node) => node.id),
  })
}

function updateEffectNode(
  document: EditorDocumentV2,
  nodeID: string,
  update: (node: EditorNodeV2) => EditorNodeV2,
) {
  if (!editorLayerSupportsEffects(document, nodeID))
    throw new EditorLayerEffectError()
  return {
    ...document,
    nodes: document.nodes.map((node) =>
      node.id === nodeID ? update(node) : node,
    ),
  }
}

function effectDefinition(type: EditorEffectTypeV2) {
  return editorEffectDefinitionsV2.find(
    (definition) => definition.type === type,
  )!
}

function formatPercent(value: number) {
  const percentage = Math.round(value * 100)
  return `${percentage >= 0 ? '+' : ''}${percentage}%`
}
