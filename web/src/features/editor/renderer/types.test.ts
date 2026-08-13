import { describe, expect, it } from 'vitest'

import { selectEditorAssetVariant } from './types'

const asset = {
  id: 'asset',
  width: 4096,
  height: 4096,
  variants: [
    { url: '/original', width: 4096, height: 4096 },
    { url: '/640', width: 640, height: 640 },
    { url: '/1280', width: 1280, height: 1280 },
  ],
}

describe('selectEditorAssetVariant', () => {
  it('selects the smallest variant satisfying screen pixels', () => {
    expect(selectEditorAssetVariant(asset, 600)?.url).toBe('/640')
    expect(selectEditorAssetVariant(asset, 900)?.url).toBe('/1280')
  })

  it('uses the largest variant only when the viewport requires it', () => {
    expect(selectEditorAssetVariant(asset, 2000)?.url).toBe('/original')
    expect(selectEditorAssetVariant(asset, 9000)?.url).toBe('/original')
  })
})
