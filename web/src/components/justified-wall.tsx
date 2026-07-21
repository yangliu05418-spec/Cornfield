import { useVirtualizer } from '@tanstack/react-virtual'
import { Copy, Download, ImagePlus, Maximize2, Trash2, X } from 'lucide-react'
import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import type {
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
} from 'react'

import type { Asset, GenerationBatch } from '#/lib/api'

const terminalJobStatuses = new Set([
  'failed',
  'cancelled',
  'submission_uncertain',
])

export type WallItem = {
  id: string
  width: number
  height: number
  asset?: Asset
  jobID?: string
  batchID?: string
  status?: string
  prompt?: string
  errorMessage?: string
  errorCode?: string
  outputIndex?: number
  cancellable?: boolean
}

type Positioned = WallItem & {
  left: number
  renderWidth: number
  renderHeight: number
}
type Row = { height: number; items: Positioned[] }

export type JustifiedWallHandle = {
  prepareLayoutChange: () => void
}

type JustifiedWallProps = {
  items: WallItem[]
  targetHeight: number
  onReference: (asset: Asset) => void
  onCancel: (batchID: string, jobID: string) => void
  onDelete: (asset: Asset) => void
  onDismiss: (batchID: string, jobID: string) => void
  onNotice?: (message: string) => void
  onLoadMore?: () => void
  hasMore?: boolean
  isLoadingMore?: boolean
}

export function makeRows(
  items: WallItem[],
  width: number,
  target: number,
  gap = 3,
): Row[] {
  if (!width || !items.length) return []
  const rows: Row[] = []
  let pending: WallItem[] = []
  let ratioSum = 0
  const flush = (last: boolean) => {
    if (!pending.length) return
    const justified = (width - gap * (pending.length - 1)) / ratioSum
    const rowHeight = last ? Math.min(target, justified) : justified
    let left = 0
    const positioned = pending.map((item, index) => {
      const renderWidth =
        index === pending.length - 1 && !last
          ? width - left
          : rowHeight * (item.width / item.height)
      const result = {
        ...item,
        left,
        renderWidth,
        renderHeight: rowHeight,
      }
      left += renderWidth + gap
      return result
    })
    rows.push({ height: rowHeight, items: positioned })
    pending = []
    ratioSum = 0
  }
  items.forEach((item) => {
    pending.push(item)
    ratioSum += item.width / item.height
    if ((width - gap * (pending.length - 1)) / ratioSum <= target) {
      flush(false)
    }
  })
  flush(true)
  return rows
}

export function buildWallItems(
  assets: Asset[],
  batches: GenerationBatch[],
): WallItem[] {
  const outputAssets = new Map<string, Asset>()
  for (const asset of assets) {
    if (asset.job_id && typeof asset.output_index === 'number') {
      outputAssets.set(`${asset.job_id}:${asset.output_index}`, asset)
    }
  }

  const claimedAssets = new Set<string>()
  const slots: WallItem[] = []
  for (const batch of batches) {
    const [ratioWidth, ratioHeight] = batch.aspect_ratio.split(':').map(Number)
    for (const job of batch.jobs) {
      if (job.dismissed_at) continue
      const deletedOutputs = new Set(job.deleted_outputs ?? [])
      for (let output = 0; output < job.expected_outputs; output++) {
        if (deletedOutputs.has(output)) continue
        const slotID = `${job.id}:${output}`
        const asset = outputAssets.get(slotID)
        if (asset) {
          claimedAssets.add(asset.id)
          slots.push({
            id: slotID,
            width: asset.width,
            height: asset.height,
            asset,
            jobID: job.id,
            batchID: batch.id,
            outputIndex: output,
          })
          continue
        }
        if (job.status === 'succeeded') continue
        const terminal = terminalJobStatuses.has(job.status)
        slots.push({
          id: slotID,
          width: ratioWidth || 1,
          height: ratioHeight || 1,
          jobID: job.id,
          batchID: batch.id,
          status: job.status,
          prompt: batch.prompt,
          errorMessage: job.error_message,
          errorCode: job.error_code,
          outputIndex: output,
          cancellable: !terminal && !batch.id.startsWith('optimistic:'),
        })
      }
    }
  }

  const remainingAssets = assets
    .filter((asset) => !claimedAssets.has(asset.id))
    .map((asset) => ({
      id: asset.id,
      width: asset.width,
      height: asset.height,
      asset,
    }))
  return [...slots, ...remainingAssets]
}

export const JustifiedWall = forwardRef<
  JustifiedWallHandle,
  JustifiedWallProps
>(function JustifiedWall(
  {
    items,
    targetHeight,
    onReference,
    onCancel,
    onDelete,
    onDismiss,
    onNotice,
    onLoadMore,
    hasMore = false,
    isLoadingMore = false,
  },
  ref,
) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const rowsRef = useRef<Row[]>([])
  const pendingAnchor = useRef<{
    id: string
    viewportOffsetY: number
  } | null>(null)
  const knownAssetIDs = useRef(new Set<string>())
  const [width, setWidth] = useState(0)
  const [preview, setPreview] = useState<Asset | null>(null)
  const [renderItems, setRenderItems] = useState(items)
  const [pendingItems, setPendingItems] = useState<WallItem[] | null>(null)
  const [newImageCount, setNewImageCount] = useState(0)

  useEffect(() => {
    const node = scrollRef.current
    if (!node) return
    const observer = new ResizeObserver(([entry]) =>
      setWidth(entry.contentRect.width),
    )
    observer.observe(node)
    return () => observer.disconnect()
  }, [])

  const prepareLayoutChange = useCallback(() => {
    const node = scrollRef.current
    const rows = rowsRef.current
    if (!node || !rows.length) return
    const viewportCenterY = node.scrollTop + node.clientHeight / 2
    const viewportCenterX = node.clientWidth / 2
    let rowTop = 0
    let selected: Positioned | undefined
    let selectedCenterY = 0
    for (const row of rows) {
      const rowBottom = rowTop + row.height + 3
      if (viewportCenterY >= rowTop && viewportCenterY < rowBottom) {
        selected = row.items.reduce(
          (closest, item) => {
            if (!closest) return item
            const currentDistance = Math.abs(
              item.left + item.renderWidth / 2 - viewportCenterX,
            )
            const closestDistance = Math.abs(
              closest.left + closest.renderWidth / 2 - viewportCenterX,
            )
            return currentDistance < closestDistance ? item : closest
          },
          undefined as Positioned | undefined,
        )
        selectedCenterY = rowTop + row.height / 2
        break
      }
      rowTop = rowBottom
    }
    if (selected) {
      pendingAnchor.current = {
        id: selected.id,
        viewportOffsetY: selectedCenterY - node.scrollTop,
      }
    }
  }, [])

  useImperativeHandle(ref, () => ({ prepareLayoutChange }), [
    prepareLayoutChange,
  ])

  useEffect(() => {
    const incomingAssetIDs = new Set(
      items.flatMap((item) => (item.asset ? [item.asset.id] : [])),
    )
    const newAssets = [...incomingAssetIDs].filter(
      (id) => !knownAssetIDs.current.has(id),
    )
    const node = scrollRef.current
    if (
      knownAssetIDs.current.size > 0 &&
      newAssets.length > 0 &&
      node &&
      node.scrollTop > 80
    ) {
      setPendingItems(items)
      setNewImageCount(newAssets.length)
      return
    }
    if (node && node.scrollTop > 80) prepareLayoutChange()
    knownAssetIDs.current = incomingAssetIDs
    setPendingItems(null)
    setNewImageCount(0)
    setRenderItems(items)
  }, [items, prepareLayoutChange])

  const rows = useMemo(
    () => makeRows(renderItems, width, targetHeight),
    [renderItems, targetHeight, width],
  )
  rowsRef.current = rows

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: (index) => (rows[index]?.height ?? targetHeight) + 3,
    overscan: 4,
  })

  const requestMoreIfNeeded = useCallback(() => {
    const node = scrollRef.current
    if (
      !node ||
      !hasMore ||
      isLoadingMore ||
      node.scrollHeight - node.scrollTop - node.clientHeight >
        node.clientHeight * 1.5
    ) {
      return
    }
    onLoadMore?.()
  }, [hasMore, isLoadingMore, onLoadMore])

  useEffect(() => {
    requestMoreIfNeeded()
  }, [rows.length, width, requestMoreIfNeeded])

  useLayoutEffect(() => {
    virtualizer.measure()
    const anchor = pendingAnchor.current
    const node = scrollRef.current
    if (!anchor || !node) return
    let rowTop = 0
    for (const row of rows) {
      if (row.items.some((item) => item.id === anchor.id)) {
        node.scrollTop = Math.max(
          0,
          rowTop + row.height / 2 - anchor.viewportOffsetY,
        )
        break
      }
      rowTop += row.height + 3
    }
    pendingAnchor.current = null
  }, [rows, virtualizer])

  function revealPendingImages() {
    if (!pendingItems) return
    prepareLayoutChange()
    knownAssetIDs.current = new Set(
      pendingItems.flatMap((item) => (item.asset ? [item.asset.id] : [])),
    )
    setRenderItems(pendingItems)
    setPendingItems(null)
    setNewImageCount(0)
  }

  return (
    <>
      <div
        className="wall-scroll"
        ref={scrollRef}
        onScroll={requestMoreIfNeeded}
      >
        {!renderItems.length && (
          <div className="wall-empty">
            <span>YOUR WALL IS QUIET</span>
            <h2>第一张图，会从这里开始。</h2>
            <p>在下方描述你的想法，生成结果将在同一位置出现。</p>
          </div>
        )}
        <div
          className="wall-stage"
          style={{ height: virtualizer.getTotalSize() }}
        >
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = rows[virtualRow.index]
            return (
              <div
                key={virtualRow.key}
                className="wall-row"
                style={{
                  height: row.height,
                  transform: `translateY(${virtualRow.start}px)`,
                }}
              >
                {row.items.map((item) => (
                  <WallCard
                    key={item.id}
                    item={item}
                    priority={virtualRow.index === 0}
                    onReference={onReference}
                    onCancel={onCancel}
                    onDelete={onDelete}
                    onDismiss={onDismiss}
                    onPreview={setPreview}
                    onNotice={onNotice}
                  />
                ))}
              </div>
            )
          })}
        </div>
      </div>
      {newImageCount > 0 && (
        <button
          className="new-images-notice"
          type="button"
          aria-live="polite"
          onClick={revealPendingImages}
        >
          {newImageCount} 张新图片
        </button>
      )}
      {preview && (
        <PreviewDialog
          asset={preview}
          onClose={() => setPreview(null)}
          onReference={onReference}
          onDelete={onDelete}
        />
      )}
    </>
  )
})

function WallCard({
  item,
  priority,
  onReference,
  onCancel,
  onDelete,
  onDismiss,
  onPreview,
  onNotice,
}: {
  item: Positioned
  priority: boolean
  onReference: (asset: Asset) => void
  onCancel: (batchID: string, jobID: string) => void
  onDelete: (asset: Asset) => void
  onDismiss: (batchID: string, jobID: string) => void
  onPreview: (asset: Asset) => void
  onNotice?: (message: string) => void
}) {
  const [imageLoaded, setImageLoaded] = useState(false)
  const style = {
    left: item.left,
    width: item.renderWidth,
    height: item.renderHeight,
  }
  if (!item.asset) {
    const terminal = terminalJobStatuses.has(item.status ?? '')
    return (
      <article
        className={`wall-card placeholder${terminal ? ' terminal' : ''}`}
        style={style}
        aria-label={`${statusLabel(item.status)}：${item.prompt ?? ''}`}
      >
        {!terminal && <div className="placeholder-shimmer" />}
        <div className="job-state">
          <span className="state-dot" />
          {statusLabel(item.status)}
        </div>
        {item.cancellable && item.jobID && item.batchID && (
          <button
            className="cancel-draw"
            type="button"
            aria-label="取消这次抽卡"
            onClick={() => onCancel(item.batchID!, item.jobID!)}
          >
            <X size={13} />
          </button>
        )}
        {terminal && item.jobID && item.batchID && (
          <div className="failed-card-overlay">
            <button
              type="button"
              aria-label="移除这次失败记录"
              onClick={() => onDismiss(item.batchID!, item.jobID!)}
            >
              <Trash2 size={14} />
            </button>
          </div>
        )}
        <div className="placeholder-copy">
          <span>{item.outputIndex! + 1}</span>
          <p>
            {terminal
              ? generationErrorMessage(item.errorCode, item.errorMessage)
              : item.prompt}
          </p>
        </div>
      </article>
    )
  }

  const asset = item.asset
  async function copyImage() {
    try {
      const copiedImage = await copyAsset(asset)
      onNotice?.(copiedImage ? '图片已复制' : '图片链接已复制')
    } catch {
      onNotice?.('复制失败，请使用下载功能')
    }
  }

  function openFromCard(
    event: ReactMouseEvent<HTMLElement> | ReactKeyboardEvent<HTMLElement>,
  ) {
    if (event.target instanceof Element && event.target.closest('button, a')) {
      return
    }
    if ('key' in event && !['Enter', ' '].includes(event.key)) return
    if ('key' in event) event.preventDefault()
    onPreview(asset)
  }

  return (
    <article
      className="wall-card"
      style={{
        ...style,
        backgroundImage: asset.blur_data_url
          ? `url(${asset.blur_data_url})`
          : undefined,
      }}
      tabIndex={0}
      aria-label="打开生成图片预览"
      onClick={openFromCard}
      onKeyDown={openFromCard}
    >
      <img
        className={imageLoaded ? 'is-loaded' : ''}
        src={asset.thumb_640_url}
        srcSet={`${asset.thumb_320_url} 320w, ${asset.thumb_640_url} 640w, ${asset.thumb_1280_url} 1280w`}
        sizes={`${Math.ceil(item.renderWidth)}px`}
        width={asset.width}
        height={asset.height}
        loading={priority ? 'eager' : 'lazy'}
        fetchPriority={priority ? 'high' : 'auto'}
        decoding="async"
        onLoad={() => setImageLoaded(true)}
        alt="生成资产"
      />
      <div className="card-overlay">
        <button type="button" onClick={() => onReference(asset)}>
          <ImagePlus size={14} />
          参考
        </button>
        <div>
          <button
            type="button"
            aria-label="放大"
            onClick={() => onPreview(asset)}
          >
            <Maximize2 size={14} />
          </button>
          <button type="button" aria-label="复制图片" onClick={copyImage}>
            <Copy size={14} />
          </button>
          <a aria-label="下载" href={`${asset.url}?download=1`}>
            <Download size={14} />
          </a>
          <button
            type="button"
            aria-label="永久删除"
            onClick={() => onDelete(asset)}
          >
            <Trash2 size={14} />
          </button>
        </div>
      </div>
    </article>
  )
}

function generationErrorMessage(code?: string, _fallback?: string): string {
  switch (code) {
    case 'CONTENT_POLICY_REJECTED':
      return '图片可能触发安全策略，请调整描述'
    case 'UNSUPPORTED_PARAMETER':
    case 'PROVIDER_HTTP_400':
    case 'PROVIDER_HTTP_413':
    case 'PROVIDER_HTTP_422':
      return '当前参数无法生成，请调整后重试'
    case 'PROVIDER_IMAGE_INVALID':
    case 'PROVIDER_RESPONSE_INVALID':
      return '生成结果无法处理，请调整参数后重试'
    case 'PROVIDER_HTTP_429':
      return '生成服务繁忙，请稍后重试'
    case 'SUBMISSION_UNCERTAIN':
    case 'SUBMISSION_INTERRUPTED':
      return '任务提交结果不确定，请等待核查或移除记录'
    default:
      return '生成失败，请稍后重试'
  }
}

function PreviewDialog({
  asset,
  onClose,
  onReference,
  onDelete,
}: {
  asset: Asset
  onClose: () => void
  onReference: (asset: Asset) => void
  onDelete: (asset: Asset) => void
}) {
  const panelRef = useRef<HTMLDivElement>(null)
  const closeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    closeRef.current?.focus()
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
        return
      }
      if (event.key !== 'Tab') return
      const controls = panelRef.current?.querySelectorAll<HTMLElement>(
        'button, a[href], [tabindex]:not([tabindex="-1"])',
      )
      if (!controls?.length) return
      const first = controls[0]
      const last = controls[controls.length - 1]
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      previous?.focus()
    }
  }, [onClose])

  return (
    <div
      className="preview-layer"
      role="dialog"
      aria-modal="true"
      aria-label="图片预览"
      onClick={onClose}
    >
      <div
        className="preview-dialog-content"
        ref={panelRef}
        onClick={(event) => event.stopPropagation()}
      >
        <button
          ref={closeRef}
          type="button"
          className="preview-close"
          aria-label="关闭预览"
          onClick={onClose}
        >
          <X />
        </button>
        <img
          src={asset.url}
          width={asset.width}
          height={asset.height}
          alt="生成结果预览"
        />
        <div className="preview-actions">
          <button
            type="button"
            onClick={() => {
              onReference(asset)
              onClose()
            }}
          >
            <ImagePlus size={15} />
            作为参考
          </button>
          <a href={`${asset.url}?download=1`}>
            <Download size={15} />
            下载原图
          </a>
          <button type="button" onClick={() => onDelete(asset)}>
            <Trash2 size={15} />
            永久删除
          </button>
        </div>
      </div>
    </div>
  )
}

async function copyAsset(asset: Asset): Promise<boolean> {
  const response = await fetch(asset.url)
  if (!response.ok) throw new Error('asset fetch failed')
  let blob = await response.blob()
  try {
    if (blob.type !== 'image/png') {
      const bitmap = await createImageBitmap(blob)
      const canvas = document.createElement('canvas')
      canvas.width = bitmap.width
      canvas.height = bitmap.height
      canvas.getContext('2d')?.drawImage(bitmap, 0, 0)
      bitmap.close()
      blob = await new Promise<Blob>((resolve, reject) =>
        canvas.toBlob(
          (result) =>
            result
              ? resolve(result)
              : reject(new Error('PNG conversion failed')),
          'image/png',
        ),
      )
    }
    await navigator.clipboard.write([new ClipboardItem({ [blob.type]: blob })])
    return true
  } catch {
    await navigator.clipboard.writeText(
      new URL(asset.url, window.location.href).href,
    )
    return false
  }
}

function statusLabel(status?: string) {
  const labels: Record<string, string> = {
    creating: '正在创建',
    queued: '排队中',
    dispatched: '准备生成',
    submitting: '正在提交',
    provider_pending: '生成中',
    ingesting: '正在保存',
    cancelling: '正在取消',
    cancelled: '已取消',
    failed: '生成失败',
    submission_uncertain: '需要核查',
  }
  return labels[status ?? ''] ?? '生成中'
}
