import { createFileRoute } from '@tanstack/react-router'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import type { InfiniteData, QueryClient } from '@tanstack/react-query'
import { Minus, Plus, Sparkles, X, ZoomIn, ZoomOut } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, FormEvent } from 'react'

import { AppShell } from '#/components/app-shell'
import { GeneratorSelect } from '#/components/generator-select'
import { buildWallItems, JustifiedWall } from '#/components/justified-wall'
import { MidjourneyOptionsControl } from '#/components/midjourney-options'
import type { JustifiedWallHandle } from '#/components/justified-wall'
import { api, getMe } from '#/lib/api'
import type {
  Asset,
  AssetPage,
  GenerationBatch,
  GenerationOptions,
  MidjourneyOptions,
  Model,
} from '#/lib/api'

export const Route = createFileRoute('/app/create')({ component: CreatePage })

const rowHeights = [180, 240, 320, 420, 560]
const generationTerminalStatuses = new Set([
  'partial',
  'succeeded',
  'failed',
  'cancelled',
])
const uploadValidationTimeout = 2 * 60 * 1000
const wallAssetsQueryKey = ['assets', 'wall'] as const

type GenerationPage = {
  items: GenerationBatch[]
  next_cursor: string
}

type AssetPages = InfiniteData<AssetPage, string>
type GenerationPages = InfiniteData<GenerationPage, string>

type PendingSubmission = {
  idempotencyKey: string
  batch: GenerationBatch
  request: {
    model_id: string
    capability_revision: string
    prompt: string
    aspect_ratio: string
    resolution: string
    draw_count: number
    input_asset_ids: string[]
    options: GenerationOptions
  }
}

function mergeAssetHead(queryClient: QueryClient, head: AssetPage): void {
  queryClient.setQueryData<AssetPages>(wallAssetsQueryKey, (current) => {
    if (!current?.pages.length) {
      return { pages: [head], pageParams: [''] }
    }
    const refreshed = new Map(head.items.map((asset) => [asset.id, asset]))
    const existing = new Set(
      current.pages.flatMap((page) => page.items.map((asset) => asset.id)),
    )
    const newAssets = head.items.filter((asset) => !existing.has(asset.id))
    const pages = current.pages.map((page) => ({
      ...page,
      items: page.items.map((asset) => refreshed.get(asset.id) ?? asset),
    }))
    pages[0] = { ...pages[0], items: [...newAssets, ...pages[0].items] }
    return { ...current, pages }
  })
}

function mergeAsset(queryClient: QueryClient, asset: Asset): void {
  queryClient.setQueryData<AssetPages>(wallAssetsQueryKey, (current) => {
    if (!current?.pages.length) {
      return {
        pages: [{ items: [asset], next_cursor: '' }],
        pageParams: [''],
      }
    }
    const found = current.pages.some((page) =>
      page.items.some((item) => item.id === asset.id),
    )
    const pages = current.pages.map((page) => ({
      ...page,
      items: page.items.map((item) => (item.id === asset.id ? asset : item)),
    }))
    if (!found) pages[0] = { ...pages[0], items: [asset, ...pages[0].items] }
    return { ...current, pages }
  })
}

function isNetworkFailure(reason: unknown): boolean {
  return reason instanceof TypeError
}

function referenceLimitLabel(bytes: number): string {
  const mebibytes = bytes / (1024 * 1024)
  return `${Number.isInteger(mebibytes) ? mebibytes : mebibytes.toFixed(1)} MiB`
}

function waitFor(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.reject(signal.reason)
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal.removeEventListener('abort', abort)
      resolve()
    }, ms)
    const abort = () => {
      window.clearTimeout(timer)
      reject(signal.reason)
    }
    signal.addEventListener('abort', abort, { once: true })
  })
}

function CreatePage() {
  const queryClient = useQueryClient()
  const wallRef = useRef<JustifiedWallHandle>(null)
  const uploadControllers = useRef(new Set<AbortController>())
  const assetRefreshInFlight = useRef<Promise<void> | null>(null)
  const assetRefreshVersion = useRef(0)
  const completionRevision = useRef<string | null>(null)
  const me = useQuery({ queryKey: ['me'], queryFn: getMe, retry: false })
  const models = useQuery({
    queryKey: ['models'],
    queryFn: () => api<{ revision: string; models: Model[] }>('/api/v1/models'),
  })
  const assets = useInfiniteQuery({
    queryKey: wallAssetsQueryKey,
    queryFn: ({ pageParam }) =>
      api<AssetPage>(
        `/api/v1/assets?limit=100${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ''}`,
      ),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    staleTime: Infinity,
  })
  const generations = useInfiniteQuery({
    queryKey: ['generations'],
    queryFn: ({ pageParam }) =>
      api<GenerationPage>(
        `/api/v1/generations?limit=100${pageParam ? `&cursor=${encodeURIComponent(pageParam)}` : ''}`,
      ),
    initialPageParam: '',
    getNextPageParam: (page) => page.next_cursor || undefined,
    refetchInterval: 10_000,
  })
  const [modelID, setModelID] = useState('')
  const [prompt, setPrompt] = useState('')
  const [ratio, setRatio] = useState('1:1')
  const [resolution, setResolution] = useState('1K')
  const [quality, setQuality] = useState('auto')
  const [draws, setDraws] = useState(1)
  const [midjourney, setMidjourney] = useState<MidjourneyOptions>({
    version: '8.1',
    resolution: 'sd',
    speed: 'fast',
    draft: false,
    stylize: 100,
    chaos: 0,
    weird: 0,
    raw: false,
    tile: false,
  })
  const [density, setDensity] = useState(2)
  const [references, setReferences] = useState<Asset[]>([])
  const [optimisticBatches, setOptimisticBatches] = useState<GenerationBatch[]>(
    [],
  )
  const [notice, setNotice] = useState('')
  const activeModel =
    models.data?.models.find((model) => model.id === modelID) ??
    models.data?.models[0]
  const maxDraws = activeModel?.capabilities.draw_count.max ?? 4
  const isMidjourney = activeModel?.id === 'legnext-midjourney'
  const refreshAssetHead = useCallback(() => {
    assetRefreshVersion.current++
    if (assetRefreshInFlight.current) return assetRefreshInFlight.current
    const drain = async (): Promise<void> => {
      const version = assetRefreshVersion.current
      const head = await api<AssetPage>('/api/v1/assets?limit=100')
      mergeAssetHead(queryClient, head)
      if (version !== assetRefreshVersion.current) await drain()
    }
    const request = drain()
      .catch(() => {
        setNotice('图片列表同步暂时失败，将在下一次状态更新时重试')
      })
      .finally(() => {
        assetRefreshInFlight.current = null
      })
    assetRefreshInFlight.current = request
    return request
  }, [queryClient])
  useEffect(
    () => () => {
      for (const controller of uploadControllers.current) controller.abort()
      uploadControllers.current.clear()
    },
    [],
  )
  useEffect(() => {
    const pages = generations.data?.pages
    const lastPage = pages?.at(-1)
    if (
      !pages ||
      !lastPage ||
      !generations.hasNextPage ||
      generations.isFetchingNextPage
    )
      return
    const shouldScanNext =
      pages.length === 1 ||
      lastPage.items.some(
        (batch) => !generationTerminalStatuses.has(batch.status),
      )
    if (shouldScanNext) void generations.fetchNextPage()
  }, [
    generations.data?.pages,
    generations.fetchNextPage,
    generations.hasNextPage,
    generations.isFetchingNextPage,
  ])
  const completedOutputsRevision = useMemo(
    () =>
      (generations.data?.pages ?? [])
        .flatMap((page) => page.items)
        .filter((batch) => batch.completed_outputs > 0)
        .map((batch) => `${batch.id}:${batch.completed_outputs}`)
        .join('|'),
    [generations.data?.pages],
  )
  useEffect(() => {
    if (completionRevision.current === null) {
      completionRevision.current = completedOutputsRevision
      if (completedOutputsRevision) void refreshAssetHead()
      return
    }
    if (completionRevision.current === completedOutputsRevision) return
    completionRevision.current = completedOutputsRevision
    void refreshAssetHead()
  }, [completedOutputsRevision, refreshAssetHead])
  useEffect(() => {
    if (!activeModel) return
    if (!modelID) setModelID(activeModel.id)
    if (!activeModel.capabilities.aspect_ratios.includes(ratio))
      setRatio(activeModel.capabilities.aspect_ratios[0] ?? 'auto')
    if (!activeModel.capabilities.resolutions.includes(resolution))
      setResolution(activeModel.capabilities.resolutions[0] ?? 'auto')
    if (!(activeModel.capabilities.qualities ?? []).includes(quality))
      setQuality(activeModel.capabilities.qualities?.[0] ?? 'auto')
    if (activeModel.id === 'legnext-midjourney') setDraws(1)
    setDraws((current) =>
      Math.min(
        activeModel.capabilities.draw_count.max,
        Math.max(activeModel.capabilities.draw_count.min, current),
      ),
    )
    setReferences((current) => {
      const limit = activeModel.capabilities.image_to_image
        ? activeModel.capabilities.max_reference_images
        : 0
      return current
        .filter(
          (asset) =>
            asset.byte_size <= activeModel.capabilities.max_reference_bytes,
        )
        .slice(0, limit)
    })
  }, [activeModel, modelID, quality, ratio, resolution])
  useEffect(() => {
    const userID = me.data?.user.id
    if (!userID) return
    const eventCursorKey = `cornfield:last-event:${userID}`
    const lastEventID = window.sessionStorage.getItem(eventCursorKey)
    const stream = new EventSource(
      lastEventID
        ? `/api/v1/events?after=${encodeURIComponent(lastEventID)}`
        : '/api/v1/events',
    )
    let refreshTimer = 0
    let refreshAssets = false
    const scheduleRefresh = (includeAssets: boolean) => {
      refreshAssets ||= includeAssets
      if (refreshTimer) return
      refreshTimer = window.setTimeout(() => {
        refreshTimer = 0
        void queryClient.invalidateQueries({ queryKey: ['generations'] })
        if (refreshAssets) {
          refreshAssets = false
          void refreshAssetHead()
        }
      }, 200)
    }
    const updateCursor = (event: MessageEvent<string>) => {
      let cursor = event.lastEventId
      if (!cursor && event.data) {
        try {
          const payload = JSON.parse(event.data) as {
            cursor?: string
            last_event_id?: string
          }
          cursor = payload.cursor ?? payload.last_event_id ?? ''
        } catch {
          // A reset event may intentionally omit a replacement cursor.
        }
      }
      if (cursor) window.sessionStorage.setItem(eventCursorKey, cursor)
      else window.sessionStorage.removeItem(eventCursorKey)
    }
    stream.addEventListener('job', (event) => {
      if (event.lastEventId) {
        window.sessionStorage.setItem(eventCursorKey, event.lastEventId)
      }
      let eventType = ''
      try {
        eventType = (JSON.parse(event.data) as { type?: string }).type ?? ''
      } catch {
        // The generations snapshot still recovers an event with invalid data.
      }
      scheduleRefresh(eventType === 'job.succeeded')
    })
    stream.addEventListener('reset', (event) => {
      if (refreshTimer) {
        window.clearTimeout(refreshTimer)
        refreshTimer = 0
      }
      updateCursor(event)
      scheduleRefresh(true)
    })
    return () => {
      if (refreshTimer) window.clearTimeout(refreshTimer)
      stream.close()
    }
  }, [me.data?.user.id, queryClient, refreshAssetHead])
  const create = useMutation({
    mutationFn: ({ idempotencyKey, request }: PendingSubmission) =>
      api<GenerationBatch>('/api/v1/generations', {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify(request),
      }),
    retry: (failureCount, reason) =>
      isNetworkFailure(reason) && failureCount < 2,
    retryDelay: (attempt) => Math.min(250 * 2 ** attempt, 1_000),
    onMutate: ({ batch }) => {
      setOptimisticBatches((current) => [batch, ...current])
    },
    onSuccess: (batch, variables) => {
      setOptimisticBatches((current) =>
        current.filter((item) => item.id !== variables.batch.id),
      )
      queryClient.setQueryData<GenerationPages>(['generations'], (current) => {
        if (!current?.pages.length) {
          return {
            pages: [{ items: [batch], next_cursor: '' }],
            pageParams: [''],
          }
        }
        const pages = current.pages.map((page) => ({
          ...page,
          items: page.items.filter((item) => item.id !== batch.id),
        }))
        pages[0] = { ...pages[0], items: [batch, ...pages[0].items] }
        return { ...current, pages }
      })
      setNotice(`${batch.expected_outputs} 个生成位置已加入画布`)
    },
    onError: (reason, variables) => {
      if (isNetworkFailure(reason)) {
        setOptimisticBatches((current) =>
          current.map((item) =>
            item.id === variables.batch.id
              ? {
                  ...item,
                  status: 'submission_uncertain',
                  jobs: item.jobs.map((job) => ({
                    ...job,
                    status: 'submission_uncertain',
                    error_message: '连接中断，请等待任务列表恢复',
                  })),
                }
              : item,
          ),
        )
        setNotice('连接中断；已使用同一请求标识重试，请勿重复提交')
        void queryClient.invalidateQueries({ queryKey: ['generations'] })
        return
      }
      setOptimisticBatches((current) =>
        current.filter((item) => item.id !== variables.batch.id),
      )
      setNotice(reason instanceof Error ? reason.message : '任务创建失败')
    },
  })
  const generationItems = useMemo(
    () => generations.data?.pages.flatMap((page) => page.items) ?? [],
    [generations.data?.pages],
  )
  useEffect(() => {
    setOptimisticBatches((current) => {
      const recovered = new Set(
        current
          .filter((batch) => batch.status === 'submission_uncertain')
          .filter((batch) =>
            generationItems.some(
              (item) =>
                item.model_id === batch.model_id &&
                item.prompt === batch.prompt &&
                item.aspect_ratio === batch.aspect_ratio &&
                item.resolution === batch.resolution &&
                item.draw_count === batch.draw_count &&
                Math.abs(
                  new Date(item.created_at).getTime() -
                    new Date(batch.created_at).getTime(),
                ) <
                  2 * 60 * 1000,
            ),
          )
          .map((batch) => batch.id),
      )
      return recovered.size
        ? current.filter((batch) => !recovered.has(batch.id))
        : current
    })
  }, [generationItems])
  const wallItems = useMemo(
    () =>
      buildWallItems(assets.data?.pages.flatMap((page) => page.items) ?? [], [
        ...optimisticBatches,
        ...generationItems,
      ]),
    [assets.data, generationItems, optimisticBatches],
  )
  function submit(event: FormEvent) {
    event.preventDefault()
    if (!prompt.trim() || !activeModel || !models.data) return
    const idempotencyKey = crypto.randomUUID()
    const optimisticID = `optimistic:${idempotencyKey}`
    const createdAt = new Date().toISOString()
    const expectedOutputs = draws * activeModel.outputs_per_draw
    const submittedResolution = isMidjourney
      ? midjourney.version === '8.1' || midjourney.version === '8'
        ? (midjourney.resolution ?? 'sd').toUpperCase()
        : 'auto'
      : resolution
    const imageOptions = activeModel.capabilities.qualities?.length
      ? { image: { quality: quality as 'auto' | 'low' | 'medium' | 'high' } }
      : {}
    const batch: GenerationBatch = {
      id: optimisticID,
      model_id: activeModel.id,
      prompt: prompt.trim(),
      aspect_ratio: ratio,
      resolution: submittedResolution,
      draw_count: draws,
      expected_outputs: expectedOutputs,
      completed_outputs: 0,
      status: 'queued',
      created_at: createdAt,
      jobs: Array.from({ length: draws }, (_, drawIndex) => ({
        id: `${optimisticID}:job:${drawIndex}`,
        draw_index: drawIndex,
        status: 'creating',
        expected_outputs: activeModel.outputs_per_draw,
      })),
      options: isMidjourney ? { midjourney } : imageOptions,
    }
    create.mutate({
      idempotencyKey,
      batch,
      request: {
        model_id: activeModel.id,
        capability_revision: models.data.revision,
        prompt: prompt.trim(),
        aspect_ratio: ratio,
        resolution: submittedResolution,
        draw_count: draws,
        input_asset_ids: references.map((asset) => asset.id),
        options: isMidjourney ? { midjourney } : imageOptions,
      },
    })
  }
  async function deleteAsset(asset: Asset) {
    if (!window.confirm('永久删除这张图片？此操作无法撤销。')) return
    try {
      await api(`/api/v1/assets/${asset.id}`, { method: 'DELETE' })
      setReferences((current) => current.filter((item) => item.id !== asset.id))
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['assets'] }),
        queryClient.invalidateQueries({ queryKey: ['generations'] }),
      ])
      setNotice('图片已进入永久删除流程')
    } catch (reason) {
      setNotice(reason instanceof Error ? reason.message : '删除失败')
    }
  }
  async function cancel(batchID: string, jobID: string) {
    try {
      const result = await api<{
        status: string
        cancel_mode: string
        cost_may_have_been_incurred: boolean
      }>(`/api/v1/generations/${batchID}/jobs/${jobID}/cancel`, {
        method: 'POST',
      })
      setNotice(
        result.cost_may_have_been_incurred
          ? '已停止等待并会丢弃迟到结果；上游可能已经产生费用'
          : '已取消生成',
      )
      void queryClient.invalidateQueries({ queryKey: ['generations'] })
    } catch (reason) {
      setNotice(reason instanceof Error ? reason.message : '取消失败')
    }
  }

  function changeDensity(next: number) {
    const normalized = Math.max(0, Math.min(4, next))
    if (normalized === density) return
    wallRef.current?.prepareLayoutChange()
    setDensity(normalized)
  }
  function addReference(asset: Asset) {
    if (!activeModel?.capabilities.image_to_image)
      return setNotice('当前模型不支持参考图')
    if (asset.byte_size > activeModel.capabilities.max_reference_bytes)
      return setNotice(
        `当前模型的单张参考图上限为 ${referenceLimitLabel(activeModel.capabilities.max_reference_bytes)}`,
      )
    setReferences((current) =>
      current.some((item) => item.id === asset.id)
        ? current
        : [...current, asset].slice(
            0,
            activeModel.capabilities.max_reference_images,
          ),
    )
    setNotice('已加入参考图')
  }
  async function uploadReference(file?: File) {
    if (!file || !activeModel?.capabilities.image_to_image)
      return setNotice('当前模型不支持参考图')
    if (file.size > activeModel.capabilities.max_reference_bytes)
      return setNotice(
        `当前模型的单张参考图上限为 ${referenceLimitLabel(activeModel.capabilities.max_reference_bytes)}`,
      )
    const controller = new AbortController()
    uploadControllers.current.add(controller)
    try {
      const session = await api<{ id: string; content_url: string }>(
        '/api/v1/uploads',
        {
          method: 'POST',
          signal: controller.signal,
          body: JSON.stringify({
            filename: file.name,
            media_type: file.type,
            size: file.size,
          }),
        },
      )
      await api(session.content_url, {
        method: 'PUT',
        body: file,
        signal: controller.signal,
      })
      let assetID = ''
      let pollDelay = 500
      const deadline = Date.now() + uploadValidationTimeout
      while (Date.now() < deadline) {
        const state = await api<{
          status: string
          asset_id?: string
          error_code?: string
        }>(`/api/v1/uploads/${session.id}`, { signal: controller.signal })
        if (state.status === 'ready' && state.asset_id) {
          assetID = state.asset_id
          break
        }
        if (state.status === 'failed')
          throw new Error(
            `参考图验证失败：${state.error_code ?? 'IMAGE_INVALID'}`,
          )
        const remaining = deadline - Date.now()
        if (remaining <= 0) break
        await waitFor(Math.min(pollDelay, remaining), controller.signal)
        pollDelay = Math.min(Math.ceil(pollDelay * 1.5), 3_000)
      }
      if (!assetID) throw new Error('参考图仍在验证，请稍后重试')
      const asset = await api<Asset>(`/api/v1/assets/${assetID}`, {
        signal: controller.signal,
      })
      mergeAsset(queryClient, asset)
      addReference(asset)
    } catch (reason) {
      if (controller.signal.aborted) return
      setNotice(reason instanceof Error ? reason.message : '参考图上传失败')
    } finally {
      uploadControllers.current.delete(controller)
    }
  }
  return (
    <AppShell>
      <main className="create-page">
        <div className="wall-toolbar">
          <div
            className="density-control"
            style={{ '--zoom-progress': `${density * 25}%` } as CSSProperties}
            role="group"
            aria-label="图片墙缩放"
          >
            <button
              type="button"
              aria-label="缩小图片"
              disabled={density === 0}
              onClick={() => changeDensity(density - 1)}
            >
              <ZoomOut size={14} />
            </button>
            <input
              aria-label="调整图片墙缩放"
              aria-valuetext={['最小', '较小', '标准', '较大', '最大'][density]}
              type="range"
              min="0"
              max="4"
              value={density}
              onChange={(event) => changeDensity(Number(event.target.value))}
            />
            <button
              type="button"
              aria-label="放大图片"
              disabled={density === 4}
              onClick={() => changeDensity(density + 1)}
            >
              <ZoomIn size={14} />
            </button>
          </div>
        </div>
        <JustifiedWall
          ref={wallRef}
          items={wallItems}
          targetHeight={rowHeights[density]}
          onReference={addReference}
          onCancel={cancel}
          onDelete={(asset) => void deleteAsset(asset)}
          onNotice={setNotice}
          hasMore={assets.hasNextPage}
          isLoadingMore={assets.isFetchingNextPage}
          onLoadMore={() => void assets.fetchNextPage()}
        />
        {notice && (
          <div className="notice" role="status">
            <span>{notice}</span>
            <button
              type="button"
              aria-label="关闭提示"
              onClick={() => setNotice('')}
            >
              <X size={12} />
            </button>
          </div>
        )}
        <form className="generator" onSubmit={submit}>
          <div className="generator-body">
            <div className="generator-prompt-row">
              <label
                className="prompt-reference-button"
                title="添加参考图"
                aria-label="添加参考图"
                aria-disabled={!activeModel?.capabilities.image_to_image}
              >
                <Plus size={17} />
                <input
                  type="file"
                  aria-label="添加参考图"
                  disabled={!activeModel?.capabilities.image_to_image}
                  accept="image/jpeg,image/png,image/webp"
                  onChange={(event) => {
                    void uploadReference(event.target.files?.[0])
                    event.target.value = ''
                  }}
                />
              </label>
              {references.length > 0 && (
                <div className="reference-strip">
                  {references.map((asset) => (
                    <div key={asset.id}>
                      <img src={asset.thumb_320_url} alt="参考图" />
                      <button
                        type="button"
                        aria-label="移除参考图"
                        onClick={() =>
                          setReferences((items) =>
                            items.filter((item) => item.id !== asset.id),
                          )
                        }
                      >
                        <X size={10} />
                      </button>
                    </div>
                  ))}
                </div>
              )}
              <textarea
                aria-label="生成提示词"
                value={prompt}
                onChange={(event) => setPrompt(event.target.value)}
                placeholder="描述你想象中的画面"
                rows={1}
              />
            </div>
            <div className="generator-controls">
              <GeneratorSelect
                label="选择模型"
                value={activeModel?.id ?? ''}
                items={(models.data?.models ?? []).map((model) => ({
                  value: model.id,
                  label: model.display_name,
                }))}
                icon={<Sparkles size={14} />}
                onChange={setModelID}
              />
              {!!activeModel?.capabilities.aspect_ratios.length && (
                <GeneratorSelect
                  label="选择画面比例"
                  value={ratio}
                  items={activeModel.capabilities.aspect_ratios.map((item) => ({
                    value: item,
                    label: item,
                  }))}
                  icon={<span className="ratio-icon" />}
                  onChange={setRatio}
                />
              )}
              {isMidjourney ? (
                <MidjourneyOptionsControl
                  value={midjourney}
                  versions={activeModel.capabilities.midjourney_versions ?? []}
                  hasReference={references.length > 0}
                  onChange={setMidjourney}
                />
              ) : activeModel?.capabilities.qualities?.length ? (
                <GeneratorSelect
                  label="选择画质"
                  value={quality}
                  items={activeModel.capabilities.qualities.map((item) => ({
                    value: item,
                    label:
                      { auto: '自动', low: '低', medium: '中', high: '高' }[
                        item
                      ] ?? item,
                  }))}
                  icon={<span className="resolution-icon" />}
                  onChange={setQuality}
                />
              ) : (
                !!activeModel?.capabilities.resolutions.length && (
                  <GeneratorSelect
                    label="选择分辨率"
                    value={resolution}
                    items={activeModel.capabilities.resolutions.map((item) => ({
                      value: item,
                      label: item,
                    }))}
                    icon={<span className="resolution-icon" />}
                    onChange={setResolution}
                  />
                )
              )}
              <div className="draw-control" aria-label="抽卡次数">
                {isMidjourney ? (
                  <span>4 张/次</span>
                ) : (
                  <>
                    <button
                      type="button"
                      disabled={draws <= 1}
                      onClick={() => setDraws(Math.max(1, draws - 1))}
                      aria-label="减少抽卡"
                    >
                      <Minus size={13} />
                    </button>
                    <span>{draws} 次</span>
                    <button
                      type="button"
                      disabled={draws >= maxDraws}
                      onClick={() => setDraws(Math.min(maxDraws, draws + 1))}
                      aria-label="增加抽卡"
                    >
                      <Plus size={13} />
                    </button>
                  </>
                )}
              </div>
            </div>
          </div>
          <button
            className="generate-button"
            disabled={!prompt.trim() || create.isPending}
          >
            {create.isPending ? '提交中…' : '生成'}
          </button>
        </form>
      </main>
    </AppShell>
  )
}
