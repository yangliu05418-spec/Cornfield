import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import type { AssetPage } from './api'
import { optimisticallyRemoveAssets, restoreAssetCaches } from './asset-cache'
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
})
