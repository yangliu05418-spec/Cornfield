import { describe, expect, it } from 'vitest'

import type { Asset } from '#/lib/api'
import { toEditorRenderAssets } from './pixi-surface'

describe('Pixi editor surface assets', () => {
  it('maps protected thumbnail variants and original dimensions deterministically', () => {
    const asset = {
      id: 'asset-1',
      width: 4000,
      height: 2000,
      thumb_320_url: '/320',
      thumb_640_url: '/640',
      thumb_1280_url: '/1280',
      url: '/original',
    } as Asset
    expect(
      toEditorRenderAssets(new Map([[asset.id, asset]])).get(asset.id),
    ).toEqual({
      id: asset.id,
      width: 4000,
      height: 2000,
      variants: [
        { url: '/320', width: 320, height: 160 },
        { url: '/640', width: 640, height: 320 },
        { url: '/1280', width: 1280, height: 640 },
        { url: '/original', width: 4000, height: 2000 },
      ],
    })
  })

  it('deduplicates fallback URLs without losing the largest known dimensions', () => {
    const asset = {
      id: 'asset-2',
      width: 640,
      height: 320,
      thumb_320_url: '/thumb',
      thumb_640_url: '/same',
      thumb_1280_url: '/same',
      url: '/same',
    } as Asset
    expect(
      toEditorRenderAssets(new Map([[asset.id, asset]])).get(asset.id)
        ?.variants,
    ).toEqual([
      { url: '/thumb', width: 320, height: 160 },
      { url: '/same', width: 640, height: 320 },
    ])
  })
})
