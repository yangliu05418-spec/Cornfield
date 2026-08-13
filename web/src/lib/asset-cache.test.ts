import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import type { AssetPage } from './api'
import {
  mergeAssetIntoCaches,
  optimisticallyRemoveAssets,
  restoreAssetCaches,
} from './asset-cache'
import type { AssetPages } from './asset-cache'

function pages(...ids: string[]): AssetPages {
  return {
    pages: [
      {
        items: ids.map((id) => ({ id })) as AssetPage['items'],
        next_cursor: '',
      },
    ],
    pageParams: [''],
  }
}

describe('asset query cache removal', () => {
  it('removes an asset from every assets query and can roll back', async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(['assets', 'wall'], pages('a', 'b'))
    queryClient.setQueryData(
      ['assets', 'library', 'active', null, ''],
      pages('a', 'c'),
    )

    const snapshot = await optimisticallyRemoveAssets(queryClient, ['a'])

    expect(
      queryClient
        .getQueryData<AssetPages>(['assets', 'wall'])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['b'])
    expect(
      queryClient
        .getQueryData<AssetPages>(['assets', 'library', 'active', null, ''])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['c'])

    restoreAssetCaches(queryClient, snapshot)
    expect(
      queryClient
        .getQueryData<AssetPages>(['assets', 'wall'])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['a', 'b'])
  })

  it('keeps completed chunks removed while restoring a failed bulk request', async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(['assets', 'wall'], pages('a', 'b', 'c'))
    const snapshot = await optimisticallyRemoveAssets(queryClient, ['a', 'b'])

    restoreAssetCaches(queryClient, snapshot, ['a'])

    expect(
      queryClient
        .getQueryData<AssetPages>(['assets', 'wall'])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['b', 'c'])
  })

  it('puts a published editor asset only into compatible active timelines', () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(['assets', 'wall'], pages('old'))
    queryClient.setQueryData(
      ['assets', 'library', 'active', null, ''],
      pages('old'),
    )
    queryClient.setQueryData(
      ['assets', 'library', 'archived', null, ''],
      pages('archived'),
    )
    queryClient.setQueryData(
      ['assets', 'library', 'active', null, 'portrait'],
      pages('match'),
    )

    mergeAssetIntoCaches(queryClient, {
      id: 'editor',
      kind: 'editor',
      media_type: 'image/png',
      width: 100,
      height: 100,
      byte_size: 10,
      sha256: 'a'.repeat(64),
      created_at: new Date().toISOString(),
      url: '/content',
      thumb_320_url: '/320',
      thumb_640_url: '/640',
      thumb_1280_url: '/1280',
    })

    expect(
      queryClient
        .getQueryData<AssetPages>(['assets', 'wall'])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['editor', 'old'])
    expect(
      queryClient
        .getQueryData<AssetPages>(['assets', 'library', 'archived', null, ''])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['archived'])
    expect(
      queryClient
        .getQueryData<AssetPages>([
          'assets',
          'library',
          'active',
          null,
          'portrait',
        ])
        ?.pages[0].items.map((asset) => asset.id),
    ).toEqual(['match'])
  })
})
