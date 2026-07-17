import { createFileRoute } from '@tanstack/react-router'
import { useInfiniteQuery } from '@tanstack/react-query'
import { Download, Search } from 'lucide-react'
import { useMemo, useState } from 'react'

import { AppShell } from '#/components/app-shell'
import { api } from '#/lib/api'
import type { AssetPage } from '#/lib/api'

export const Route = createFileRoute('/app/assets')({ component: AssetsPage })

function AssetsPage() {
  const [search, setSearch] = useState('')
  const assets = useInfiniteQuery({
    queryKey: ['assets', 'library'],
    queryFn: ({ pageParam }) =>
      api<AssetPage>(
        `/api/v1/assets?limit=100${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ''}`,
      ),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
  })
  const items = useMemo(() => {
    const normalizedSearch = search.trim().toLowerCase()
    return (assets.data?.pages.flatMap((page) => page.items) ?? []).filter(
      (asset) =>
        !normalizedSearch ||
        asset.original_filename?.toLowerCase().includes(normalizedSearch),
    )
  }, [assets.data, search])

  return (
    <AppShell>
      <main className="library-page">
        <header className="page-heading">
          <div>
            <p className="eyebrow">PRIVATE LIBRARY</p>
            <h1>你的资产</h1>
            <p>所有生成结果与参考图，按最近使用排序。</p>
          </div>
          <label className="search-box">
            <Search size={14} />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="搜索文件名"
            />
          </label>
        </header>
        <div className="asset-grid">
          {items.map((asset) => (
            <article key={asset.id}>
              <div className="asset-image">
                <img
                  src={asset.thumb_640_url}
                  width={asset.width}
                  height={asset.height}
                  alt="资产缩略图"
                  loading="lazy"
                  decoding="async"
                />
                <a href={`${asset.url}?download=1`} aria-label="下载">
                  <Download size={14} />
                </a>
              </div>
              <div>
                <span>
                  {asset.kind === 'generation' ? 'GENERATED' : 'REFERENCE'}
                </span>
                <p>
                  {asset.width} × {asset.height}
                </p>
              </div>
            </article>
          ))}
        </div>
        {assets.hasNextPage && (
          <button
            type="button"
            className="library-load-more"
            disabled={assets.isFetchingNextPage}
            onClick={() => void assets.fetchNextPage()}
          >
            {assets.isFetchingNextPage ? '加载中…' : '加载更多'}
          </button>
        )}
        {!assets.isLoading && !items.length && (
          <div className="table-empty">
            还没有资产。回到创作页生成第一张图片。
          </div>
        )}
      </main>
    </AppShell>
  )
}
