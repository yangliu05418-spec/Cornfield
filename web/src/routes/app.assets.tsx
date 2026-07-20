import { createFileRoute } from '@tanstack/react-router'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import type { InfiniteData } from '@tanstack/react-query'
import {
  Archive,
  ArchiveRestore,
  Download,
  Folder,
  FolderPlus,
  Pencil,
  Search,
  Trash2,
  X,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import type { DragEvent, FormEvent } from 'react'

import { AppShell } from '#/components/app-shell'
import { api } from '#/lib/api'
import type { Asset, AssetFolder, AssetPage } from '#/lib/api'

export const Route = createFileRoute('/app/assets')({ component: AssetsPage })

type AssetView = 'active' | 'archived' | 'all'
type AssetPages = InfiniteData<AssetPage, string>
type OrganizationChange = {
  asset: Asset
  folder_id?: string | null
  archived?: boolean
}

function AssetsPage() {
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [view, setView] = useState<AssetView>('active')
  const [folderID, setFolderID] = useState<string | null>(null)
  const [notice, setNotice] = useState('')
  const [folderEditor, setFolderEditor] = useState<AssetFolder | 'new' | null>(
    null,
  )
  const folderQuery = useQuery({
    queryKey: ['asset-folders'],
    queryFn: () => api<{ items: AssetFolder[] }>('/api/v1/asset-folders'),
  })
  const queryKey = ['assets', 'library', view, folderID] as const
  const assets = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams({ limit: '100', view })
      if (folderID) params.set('folder_id', folderID)
      if (pageParam) params.set('cursor', pageParam)
      return api<AssetPage>(`/api/v1/assets?${params}`)
    },
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
  })
  const organization = useMutation({
    mutationFn: ({ asset, ...body }: OrganizationChange) =>
      api(`/api/v1/assets/${asset.id}/organization`, {
        method: 'PATCH',
        body: JSON.stringify(body),
      }),
    onMutate: async (change) => {
      await queryClient.cancelQueries({ queryKey })
      const previous = queryClient.getQueryData<AssetPages>(queryKey)
      queryClient.setQueryData<AssetPages>(queryKey, (current) => {
        if (!current) return current
        const pages = current.pages.map((page) => ({
          ...page,
          items: page.items.flatMap((asset) => {
            if (asset.id !== change.asset.id) return [asset]
            const updated = {
              ...asset,
              ...(change.folder_id !== undefined
                ? { folder_id: change.folder_id ?? undefined }
                : {}),
              ...(change.archived !== undefined
                ? {
                    archived_at: change.archived
                      ? new Date().toISOString()
                      : undefined,
                  }
                : {}),
            }
            const visibleInView =
              view === 'all' ||
              (view === 'active' && !updated.archived_at) ||
              (view === 'archived' && !!updated.archived_at)
            const visibleInFolder = !folderID || updated.folder_id === folderID
            return visibleInView && visibleInFolder ? [updated] : []
          }),
        }))
        return { ...current, pages }
      })
      return { previous }
    },
    onError: (error, _change, context) => {
      if (context?.previous)
        queryClient.setQueryData(queryKey, context.previous)
      setNotice(error.message)
    },
    onSuccess: () => setNotice('资产组织已更新'),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: ['assets'] })
      void queryClient.invalidateQueries({ queryKey: ['asset-folders'] })
    },
  })
  const saveFolder = useMutation({
    mutationFn: ({ id, name }: { id?: string; name: string }) =>
      api(id ? `/api/v1/asset-folders/${id}` : '/api/v1/asset-folders', {
        method: id ? 'PATCH' : 'POST',
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => {
      setFolderEditor(null)
      void queryClient.invalidateQueries({ queryKey: ['asset-folders'] })
    },
  })
  const items = useMemo(() => {
    const normalizedSearch = search.trim().toLowerCase()
    return (assets.data?.pages.flatMap((page) => page.items) ?? []).filter(
      (asset) =>
        !normalizedSearch ||
        asset.original_filename?.toLowerCase().includes(normalizedSearch),
    )
  }, [assets.data, search])

  function moveAsset(asset: Asset, targetFolderID: string | null) {
    organization.mutate({ asset, folder_id: targetFolderID })
  }
  function dropInto(event: DragEvent, targetFolderID: string | null) {
    event.preventDefault()
    const asset = items.find(
      (item) =>
        item.id === event.dataTransfer.getData('application/x-asset-id'),
    )
    if (asset) moveAsset(asset, targetFolderID)
  }
  async function deleteAsset(id: string) {
    if (!window.confirm('永久删除这张图片？此操作无法撤销。')) return
    try {
      await api(`/api/v1/assets/${id}`, { method: 'DELETE' })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['assets'] }),
        queryClient.invalidateQueries({ queryKey: ['generations'] }),
        queryClient.invalidateQueries({ queryKey: ['asset-folders'] }),
      ])
      setNotice('图片已进入永久删除流程')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '删除失败')
    }
  }
  async function deleteFolder(folder: AssetFolder) {
    if (!window.confirm(`删除文件夹“${folder.name}”？其中图片会移回未归档。`))
      return
    try {
      await api(`/api/v1/asset-folders/${folder.id}`, { method: 'DELETE' })
      if (folderID === folder.id) setFolderID(null)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['asset-folders'] }),
        queryClient.invalidateQueries({ queryKey: ['assets'] }),
      ])
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '删除文件夹失败')
    }
  }

  return (
    <AppShell>
      <main className="library-page organized-library">
        <header className="page-heading">
          <div>
            <p className="eyebrow">PRIVATE LIBRARY</p>
            <h1>资产工作台</h1>
            <p>归档、分组与整理生成结果，不打断创作墙。</p>
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

        <div className="asset-workspace">
          <aside className="folder-sidebar">
            <div className="asset-view-switch" aria-label="资产视图">
              {(
                [
                  ['active', '未归档'],
                  ['all', '全部'],
                  ['archived', '已归档'],
                ] as [AssetView, string][]
              ).map(([value, label]) => (
                <button
                  type="button"
                  data-active={view === value}
                  key={value}
                  onClick={() => setView(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            <div className="folder-heading">
              <span>文件夹</span>
              <button
                type="button"
                aria-label="新建文件夹"
                onClick={() => setFolderEditor('new')}
              >
                <FolderPlus size={14} />
              </button>
            </div>
            <button
              type="button"
              className="folder-row"
              data-active={!folderID}
              onClick={() => setFolderID(null)}
              onDragOver={(event) => event.preventDefault()}
              onDrop={(event) => dropInto(event, null)}
            >
              <Folder size={14} />
              <span>所有文件</span>
            </button>
            {folderQuery.data?.items.map((folder) => (
              <div
                className="folder-row-wrap"
                key={folder.id}
                onDragOver={(event) => event.preventDefault()}
                onDrop={(event) => dropInto(event, folder.id)}
              >
                <button
                  type="button"
                  className="folder-row"
                  data-active={folderID === folder.id}
                  onClick={() => setFolderID(folder.id)}
                >
                  <Folder size={14} />
                  <span>{folder.name}</span>
                  <small>{folder.asset_count}</small>
                </button>
                <div>
                  <button
                    type="button"
                    aria-label={`重命名 ${folder.name}`}
                    onClick={() => setFolderEditor(folder)}
                  >
                    <Pencil size={12} />
                  </button>
                  <button
                    type="button"
                    aria-label={`删除 ${folder.name}`}
                    onClick={() => void deleteFolder(folder)}
                  >
                    <Trash2 size={12} />
                  </button>
                </div>
              </div>
            ))}
          </aside>

          <section className="asset-collection">
            <div className="asset-grid">
              {items.map((asset) => (
                <article
                  key={asset.id}
                  draggable
                  onDragStart={(event) => {
                    event.dataTransfer.setData(
                      'application/x-asset-id',
                      asset.id,
                    )
                    event.dataTransfer.effectAllowed = 'move'
                    const image = event.currentTarget.querySelector('img')
                    if (image) event.dataTransfer.setDragImage(image, 56, 56)
                  }}
                >
                  <div className="asset-image">
                    <img
                      src={asset.thumb_640_url}
                      width={asset.width}
                      height={asset.height}
                      alt="资产缩略图"
                      loading="lazy"
                      decoding="async"
                    />
                    <div className="asset-card-actions">
                      <a href={`${asset.url}?download=1`} aria-label="下载">
                        <Download size={14} />
                      </a>
                      <button
                        type="button"
                        aria-label={asset.archived_at ? '取消归档' : '归档'}
                        onClick={() =>
                          organization.mutate({
                            asset,
                            archived: !asset.archived_at,
                          })
                        }
                      >
                        {asset.archived_at ? (
                          <ArchiveRestore size={14} />
                        ) : (
                          <Archive size={14} />
                        )}
                      </button>
                      <button
                        type="button"
                        aria-label="永久删除"
                        onClick={() => void deleteAsset(asset.id)}
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </div>
                  <div className="asset-card-meta">
                    <span>
                      {asset.kind === 'generation' ? 'GENERATED' : 'REFERENCE'}
                    </span>
                    <label>
                      <span className="sr-only">移动到文件夹</span>
                      <select
                        aria-label="移动到文件夹"
                        value={asset.folder_id ?? ''}
                        onChange={(event) =>
                          moveAsset(asset, event.target.value || null)
                        }
                      >
                        <option value="">未分组</option>
                        {folderQuery.data?.items.map((folder) => (
                          <option key={folder.id} value={folder.id}>
                            {folder.name}
                          </option>
                        ))}
                      </select>
                    </label>
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
              <div className="table-empty">这个视图还没有资产。</div>
            )}
          </section>
        </div>

        {notice && (
          <div className="notice" role="status">
            <span>{notice}</span>
            <button aria-label="关闭提示" onClick={() => setNotice('')}>
              <X size={12} />
            </button>
          </div>
        )}
        {folderEditor && (
          <FolderEditor
            folder={folderEditor}
            busy={saveFolder.isPending}
            error={saveFolder.error?.message}
            onClose={() => setFolderEditor(null)}
            onSave={(name) =>
              saveFolder.mutate({
                id: folderEditor === 'new' ? undefined : folderEditor.id,
                name,
              })
            }
          />
        )}
      </main>
    </AppShell>
  )
}

function FolderEditor({
  folder,
  busy,
  error,
  onClose,
  onSave,
}: {
  folder: AssetFolder | 'new'
  busy: boolean
  error?: string
  onClose: () => void
  onSave: (name: string) => void
}) {
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const name = String(new FormData(event.currentTarget).get('name')).trim()
    if (name) onSave(name)
  }
  return (
    <div className="modal-layer">
      <section className="admin-modal folder-modal">
        <button className="modal-close" aria-label="关闭" onClick={onClose}>
          <X />
        </button>
        <p className="eyebrow">ASSET FOLDER</p>
        <h2>{folder === 'new' ? '新建文件夹' : '重命名文件夹'}</h2>
        <form onSubmit={submit}>
          <label>
            名称
            <input
              autoFocus
              name="name"
              maxLength={64}
              defaultValue={folder === 'new' ? '' : folder.name}
              required
            />
          </label>
          {error && <p className="form-error">{error}</p>}
          <button className="primary-button" disabled={busy}>
            {busy ? '保存中…' : '保存'}
          </button>
        </form>
      </section>
    </div>
  )
}
