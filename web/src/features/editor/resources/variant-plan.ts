import { selectEditorAssetVariant } from '../renderer/types'
import type { EditorDocument, EditorObject } from '../domain/document'
import type {
  EditorAssetVariant,
  EditorRenderAsset,
  EditorViewport,
} from '../renderer/types'

export type EditorVariantPlan = {
  variants: ReadonlyMap<string, EditorAssetVariant>
  estimatedBytes: number
  budgetExceeded: boolean
}

type Candidate = {
  key: string
  objectIDs: string[]
  variants: EditorAssetVariant[]
  index: number
}

export function planEditorAssetVariants(
  document: EditorDocument,
  assets: ReadonlyMap<string, EditorRenderAsset>,
  viewport: EditorViewport,
  resolution: number,
  budgetBytes: number,
): EditorVariantPlan {
  const candidatesByAsset = new Map<string, Candidate>()
  for (const object of document.objects) {
    if (!object.visible || object.opacity === 0) continue
    const asset = assets.get(object.asset_id)
    if (!asset || asset.variants.length === 0) continue
    const variants = [...asset.variants].sort(
      (left, right) => variantPixels(left) - variantPixels(right),
    )
    const desired = selectEditorAssetVariant(
      asset,
      requiredEditorAssetPixels(object, asset, viewport, resolution),
    )
    const desiredIndex = Math.max(0, variants.indexOf(desired!))
    const candidate = candidatesByAsset.get(asset.id)
    if (candidate) {
      candidate.objectIDs.push(object.id)
      candidate.index = Math.max(candidate.index, desiredIndex)
    } else {
      candidatesByAsset.set(asset.id, {
        key: asset.id,
        objectIDs: [object.id],
        variants,
        index: desiredIndex,
      })
    }
  }
  const candidates = [...candidatesByAsset.values()]
  let bytes = plannedBytes(candidates)
  while (bytes > budgetBytes) {
    let best: { candidate: Candidate; bytes: number } | undefined
    for (const candidate of candidates) {
      if (candidate.index === 0) continue
      candidate.index -= 1
      const nextBytes = plannedBytes(candidates)
      candidate.index += 1
      if (
        nextBytes < bytes &&
        (!best ||
          nextBytes < best.bytes ||
          (nextBytes === best.bytes &&
            candidate.key.localeCompare(best.candidate.key) < 0))
      ) {
        best = { candidate, bytes: nextBytes }
      }
    }
    if (!best) break
    best.candidate.index -= 1
    bytes = best.bytes
  }
  return {
    variants: new Map(
      candidates.flatMap((candidate) =>
        candidate.objectIDs.map((objectID) => [
          objectID,
          candidate.variants[candidate.index],
        ]),
      ),
    ),
    estimatedBytes: bytes,
    budgetExceeded: bytes > budgetBytes,
  }
}

export function requiredEditorAssetPixels(
  object: EditorObject,
  asset: EditorRenderAsset,
  viewport: EditorViewport,
  resolution: number,
) {
  const scaleX = Math.hypot(object.transform[0], object.transform[1])
  const scaleY = Math.hypot(object.transform[2], object.transform[3])
  const viewportScale = (viewport.zoom / 100) * resolution
  return Math.ceil(
    Math.max(asset.width * scaleX, asset.height * scaleY) * viewportScale,
  )
}

function plannedBytes(candidates: readonly Candidate[]) {
  const unique = new Map<string, number>()
  for (const candidate of candidates) {
    const variant = candidate.variants[candidate.index]
    unique.set(variant.url, variant.width * variant.height * 4)
  }
  return [...unique.values()].reduce((total, bytes) => total + bytes, 0)
}

function variantPixels(variant: EditorAssetVariant) {
  return Math.max(variant.width, variant.height)
}
