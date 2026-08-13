import type { EditorDocument } from '../domain/document'

export type EditorViewport = {
  zoom: number
  panX: number
  panY: number
}

export type EditorAssetVariant = {
  url: string
  width: number
  height: number
}

export type EditorRenderAsset = {
  id: string
  width: number
  height: number
  variants: EditorAssetVariant[]
}

export type EditorRendererStats = {
  nodes: number
  textures: number
  estimatedTextureBytes: number
  activeTextureBytes: number
  textureBudgetBytes: number
  textureBudgetExceeded: boolean
  contextLost: boolean
}

export type EditorRendererOptions = {
  width: number
  height: number
  resolution?: number
  textureBudgetBytes?: number
  resolutionUpgradeDelayMs?: number
  preserveDrawingBuffer?: boolean
  onContextChange?: (lost: boolean) => void
  onError?: (error: unknown) => void
}

export interface EditorRenderer {
  init: (
    canvas: HTMLCanvasElement,
    options: EditorRendererOptions,
  ) => Promise<void>
  sync: (
    document: EditorDocument,
    assets: ReadonlyMap<string, EditorRenderAsset>,
  ) => Promise<void>
  setViewport: (viewport: EditorViewport) => void
  render: () => void
  stats: () => EditorRendererStats
  settleResources: () => Promise<void>
  destroy: () => void
}

export function selectEditorAssetVariant(
  asset: EditorRenderAsset,
  requiredPixels: number,
) {
  const variants = [...asset.variants].sort(
    (a, b) => Math.max(a.width, a.height) - Math.max(b.width, b.height),
  )
  return (
    variants.find(
      (variant) => Math.max(variant.width, variant.height) >= requiredPixels,
    ) ?? variants.at(-1)
  )
}
