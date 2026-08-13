import type { InfiniteData, QueryClient, QueryKey } from '@tanstack/react-query'

import type { Asset, AssetPage } from './api'

export type AssetPages = InfiniteData<AssetPage, string>
export type AssetCacheSnapshot = Array<[QueryKey, AssetPages | undefined]>

export function removeAssetsFromPages(
  current: AssetPages | undefined,
  assetIDs: ReadonlySet<string>,
): AssetPages | undefined {
  if (!current) return current
  return {
    ...current,
    pages: current.pages.map((page) => ({
      ...page,
      items: page.items.filter((asset) => !assetIDs.has(asset.id)),
    })),
  }
}

export async function optimisticallyRemoveAssets(
  queryClient: QueryClient,
  assetIDs: Iterable<string>,
): Promise<AssetCacheSnapshot> {
  const ids = new Set(assetIDs)
  await queryClient.cancelQueries({ queryKey: ['assets'] })
  const snapshot = queryClient.getQueriesData<AssetPages>({
    queryKey: ['assets'],
  })
  queryClient.setQueriesData<AssetPages>({ queryKey: ['assets'] }, (current) =>
    removeAssetsFromPages(current, ids),
  )
  return snapshot
}

export function restoreAssetCaches(
  queryClient: QueryClient,
  snapshot: AssetCacheSnapshot,
  keepRemoved: Iterable<string> = [],
): void {
  const removed = new Set(keepRemoved)
  for (const [queryKey, value] of snapshot) {
    queryClient.setQueryData(queryKey, removeAssetsFromPages(value, removed))
  }
}

export function mergeAssetIntoCaches(
  queryClient: QueryClient,
  asset: Asset,
): void {
  for (const [queryKey, current] of queryClient.getQueriesData<AssetPages>({
    queryKey: ['assets'],
  })) {
    if (!current || !acceptsNewActiveAsset(queryKey)) continue
    const pages = current.pages.map((page) => ({
      ...page,
      items: page.items.filter((item) => item.id !== asset.id),
    }))
    if (!pages.length) continue
    pages[0] = { ...pages[0], items: [asset, ...pages[0].items] }
    queryClient.setQueryData(queryKey, { ...current, pages })
  }
}

function acceptsNewActiveAsset(queryKey: QueryKey): boolean {
  if (queryKey[0] !== 'assets') return false
  if (queryKey[1] === 'wall') return true
  if (queryKey[1] !== 'library') return false
  const view = queryKey[2]
  const folderID = queryKey[3]
  const search = queryKey[4]
  return (view === 'active' || view === 'all') && !folderID && !search
}
