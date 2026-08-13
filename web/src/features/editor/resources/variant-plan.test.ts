import { describe, expect, it } from 'vitest'

import { planEditorAssetVariants } from './variant-plan'
import type { EditorDocument } from '../domain/document'
import type { EditorRenderAsset } from '../renderer/types'

const asset = (id: string): EditorRenderAsset => ({
  id,
  width: 4096,
  height: 4096,
  variants: [
    { url: `/${id}/640`, width: 640, height: 640 },
    { url: `/${id}/1280`, width: 1280, height: 1280 },
    { url: `/${id}/original`, width: 4096, height: 4096 },
  ],
})

const document: EditorDocument = {
  schema_version: 1,
  canvas: { width: 6000, height: 6000 },
  objects: ['a', 'b', 'c'].map((id, z_index) => ({
    id,
    asset_id: id,
    transform: [1, 0, 0, 1, 0, 0],
    opacity: 1,
    visible: true,
    locked: false,
    z_index,
  })),
}

describe('planEditorAssetVariants', () => {
  it('selects desired variants when they fit the global budget', () => {
    const plan = planEditorAssetVariants(
      document,
      new Map(['a', 'b', 'c'].map((id) => [id, asset(id)])),
      { zoom: 20, panX: 0, panY: 0 },
      1,
      64 << 20,
    )
    expect([...plan.variants.values()].map((variant) => variant.width)).toEqual(
      [1280, 1280, 1280],
    )
    expect(plan.budgetExceeded).toBe(false)
  })

  it('deterministically downgrades variants to stay within budget', () => {
    const plan = planEditorAssetVariants(
      document,
      new Map(['a', 'b', 'c'].map((id) => [id, asset(id)])),
      { zoom: 100, panX: 0, panY: 0 },
      1,
      9 << 20,
    )
    expect(plan.estimatedBytes).toBeLessThanOrEqual(9 << 20)
    expect([...plan.variants.values()].map((variant) => variant.width)).toEqual(
      [640, 640, 640],
    )
  })

  it('accounts for shared texture URLs only once', () => {
    const shared = asset('shared')
    const sharedDocument: EditorDocument = {
      ...document,
      objects: document.objects.map((object) => ({
        ...object,
        asset_id: 'shared',
      })),
    }
    const plan = planEditorAssetVariants(
      sharedDocument,
      new Map([['shared', shared]]),
      { zoom: 20, panX: 0, panY: 0 },
      1,
      7 << 20,
    )
    expect(plan.estimatedBytes).toBe(1280 * 1280 * 4)
  })

  it('downgrades every reference to a shared asset as one texture group', () => {
    const shared = asset('shared')
    const sharedDocument: EditorDocument = {
      ...document,
      objects: document.objects.map((object) => ({
        ...object,
        asset_id: 'shared',
      })),
    }
    const plan = planEditorAssetVariants(
      sharedDocument,
      new Map([['shared', shared]]),
      { zoom: 100, panX: 0, panY: 0 },
      1,
      2 << 20,
    )
    expect(plan.estimatedBytes).toBe(640 * 640 * 4)
    expect([...plan.variants.values()].map((variant) => variant.width)).toEqual(
      [640, 640, 640],
    )
  })
})
