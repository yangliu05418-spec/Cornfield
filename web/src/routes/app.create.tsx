import { createFileRoute, useNavigate } from '@tanstack/react-router'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import type { InfiniteData, QueryClient } from '@tanstack/react-query'
import { Minus, Plus, Sparkles, Trash2, X, ZoomIn, ZoomOut } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import type { ClipboardEvent, CSSProperties, DragEvent, FormEvent } from 'react'

import { AppShell } from '#/components/app-shell'
import { ConfirmDialog } from '#/components/confirm-dialog'
import { GeneratorSelect } from '#/components/generator-select'
import {
  buildWallItems,
  canRefineGenerationError,
  JustifiedWall,
} from '#/components/justified-wall'
import { MidjourneyOptionsControl } from '#/components/midjourney-options'
import { PromptRefinerIcon } from '#/components/prompt-refiner-icon'
import {
  mapRefinedSelection,
  PromptRefinerReview,
} from '#/components/prompt-refiner-review'
import type { JustifiedWallHandle } from '#/components/justified-wall'
import { api, APIError, getMe } from '#/lib/api'
import { creationDraft } from '#/lib/creation-draft'
import {
  optimisticallyRemoveAssets,
  restoreAssetCaches,
} from '#/lib/asset-cache'
import {
  clipboardImageFiles,
  normalizeReferenceMediaType,
  referenceUploadErrorMessage,
} from '#/lib/reference-upload'
import type {
  Asset,
  AssetPage,
  GenerationBatch,
  GenerationJob,
  GenerationOptions,
  MidjourneyOptions,
  Model,
  PromptRefineResponse,
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
const dismissibleGenerationStatuses = new Set([
  'failed',
  'cancelled',
  'submission_uncertain',
])

function resizePromptTextarea(textarea: HTMLTextAreaElement | null) {
  if (!textarea) return
  textarea.style.height = '0px'
  const configuredMax = Number.parseFloat(
    window.getComputedStyle(textarea).maxHeight,
  )
  const maxHeight = Number.isFinite(configuredMax) ? configuredMax : 136
  const nextHeight = Math.max(40, Math.min(textarea.scrollHeight, maxHeight))
  textarea.style.height = `${nextHeight}px`
  textarea.style.overflowY =
    textarea.scrollHeight > maxHeight ? 'auto' : 'hidden'
}

type GenerationPage = {
  items: GenerationBatch[]
  next_cursor: string
}

type AssetPages = InfiniteData<AssetPage, string>
type GenerationPages = InfiniteData<GenerationPage, string>

type PendingSubmission = {
  idempotencyKey: string
  batch: GenerationBatch
  refinementID?: string
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

type GenerationRequest = PendingSubmission['request']

type AppliedPromptRefinement = {
  refinementID?: string
  before: string
  after: string
  selection: { start: number; end: number }
  controlSignature: string
}

export function submittedRefinementID(
  refinement: Pick<AppliedPromptRefinement, 'refinementID' | 'after'> | null,
  submittedPrompt: string,
) {
  return refinement?.refinementID && refinement.after.trim() === submittedPrompt
    ? refinement.refinementID
    : undefined
}

function reportPromptRefinementFeedback(
  refinementID: string | undefined,
  feedback: { event: 'undone' } | { event: 'submitted'; batch_id: string },
) {
  if (!refinementID) return
  void api<void>('/api/v1/prompts/refinements/feedback', {
    method: 'POST',
    body: JSON.stringify({ refinement_id: refinementID, ...feedback }),
  }).catch(() => undefined)
}

type ReferenceItem =
  | { key: string; source: 'asset'; asset: Asset }
  | {
      key: string
      source: 'local'
      file: File
      previewURL: string
      mediaType: string
      width?: number
      height?: number
    }

function assetReference(asset: Asset): ReferenceItem {
  return { key: `asset:${asset.id}`, source: 'asset', asset }
}

function referenceByteSize(reference: ReferenceItem): number {
  return reference.source === 'asset'
    ? reference.asset.byte_size
    : reference.file.size
}

function referencePreviewStyle(reference: ReferenceItem): CSSProperties {
  const width =
    reference.source === 'asset' ? reference.asset.width : reference.width
  const height =
    reference.source === 'asset' ? reference.asset.height : reference.height
  return width && height ? { aspectRatio: `${width} / ${height}` } : {}
}

function uploadedReferenceIDs(references: ReferenceItem[]): string[] {
  return references.flatMap((reference) =>
    reference.source === 'asset' ? [reference.asset.id] : [],
  )
}

type JobEventEnvelope = {
  id: number
  type: string
  batch_id?: string
  job_id?: string
  payload?: {
    status?: string
    error_code?: string
    message?: string
    completed_outputs?: number
    dismissed?: boolean
    outputs?: GenerationJob['outputs']
    assets?: Asset[]
  }
}

export function generationImageOptions(
  model: Model,
  quality: string,
  promptOptimizationMode: 'standard' | 'fast',
): GenerationOptions {
  const image: NonNullable<GenerationOptions['image']> = {}
  if (model.capabilities.qualities?.length)
    image.quality = quality as NonNullable<typeof image.quality>
  if (model.capabilities.prompt_optimization_modes?.length)
    image.prompt_optimization_mode = promptOptimizationMode
  return Object.keys(image).length ? { image } : {}
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

export function mergeGenerationBatch(
  queryClient: QueryClient,
  incoming: GenerationBatch,
): void {
  queryClient.setQueryData<GenerationPages>(['generations'], (current) => {
    if (!current?.pages.length) {
      return {
        pages: [{ items: [incoming], next_cursor: '' }],
        pageParams: [''],
      }
    }
    const found = current.pages.some((page) =>
      page.items.some((batch) => batch.id === incoming.id),
    )
    const pages = current.pages.map((page) => ({
      ...page,
      items: page.items.map((batch) => {
        if (batch.id !== incoming.id) return batch
        return incoming
      }),
    }))
    if (!found) pages[0] = { ...pages[0], items: [incoming, ...pages[0].items] }
    return { ...current, pages }
  })
}

export function applyGenerationEvent(
  queryClient: QueryClient,
  event: JobEventEnvelope,
): void {
  const payload = event.payload ?? {}
  for (const asset of payload.assets ?? []) mergeAsset(queryClient, asset)
  if (!event.batch_id) return
  queryClient.setQueryData<GenerationPages>(['generations'], (current) => {
    if (!current) return current
    return {
      ...current,
      pages: current.pages.map((page) => ({
        ...page,
        items: page.items.map((batch) => {
          if (batch.id !== event.batch_id) return batch
          const jobs = batch.jobs.map((job) => {
            if (!event.job_id || job.id !== event.job_id) return job
            return {
              ...job,
              ...(payload.status ? { status: payload.status } : {}),
              ...(payload.error_code ? { error_code: payload.error_code } : {}),
              ...(payload.message ? { error_message: payload.message } : {}),
              ...(payload.outputs ? { outputs: payload.outputs } : {}),
              ...(payload.dismissed
                ? { dismissed_at: new Date().toISOString() }
                : {}),
            }
          })
          return {
            ...batch,
            jobs,
            ...(payload.completed_outputs !== undefined
              ? { completed_outputs: payload.completed_outputs }
              : {}),
            ...(!event.job_id && payload.status
              ? { status: payload.status }
              : {}),
          }
        }),
      })),
    }
  })
}

function isNetworkFailure(reason: unknown): boolean {
  return reason instanceof TypeError
}

export function failedJobAction(job: GenerationJob): 'retry' | 'edit' | 'none' {
  if (job.status !== 'failed') return 'none'
  return job.retryable ? 'retry' : 'edit'
}

export function recentRepeatedPolicyFailure(
  request: GenerationRequest,
  batches: GenerationBatch[],
  now = Date.now(),
): GenerationBatch | undefined {
  const cutoff = now - 30 * 60 * 1000
  return batches.find(
    (batch) =>
      new Date(batch.created_at).getTime() >= cutoff &&
      batch.model_id === request.model_id &&
      batch.prompt.trim() === request.prompt.trim() &&
      batch.aspect_ratio === request.aspect_ratio &&
      batch.resolution === request.resolution &&
      JSON.stringify(batch.options ?? {}) === JSON.stringify(request.options) &&
      batch.jobs.some(
        (job) =>
          job.status === 'failed' &&
          job.error_code === 'CONTENT_POLICY_REJECTED',
      ),
  )
}

function policyRetrySignature(request: GenerationRequest): string {
  return JSON.stringify({
    model_id: request.model_id,
    prompt: request.prompt.trim(),
    aspect_ratio: request.aspect_ratio,
    resolution: request.resolution,
    options: request.options,
  })
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
  const streamConnected = useRef(false)
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const wallRef = useRef<JustifiedWallHandle>(null)
  const promptRef = useRef<HTMLTextAreaElement>(null)
  const promptDragDepth = useRef(0)
  const uploadControllers = useRef(new Set<AbortController>())
  const localReferenceURLs = useRef(new Set<string>())
  const assetRefreshInFlight = useRef<Promise<void> | null>(null)
  const assetRefreshVersion = useRef(0)
  const assetRecoveryRevision = useRef('')
  const repeatedPolicyBypass = useRef('')
  const refinerAbort = useRef<AbortController | null>(null)
  const refinerBusyRef = useRef(false)
  const refinerPendingSignature = useRef('')
  const refinerRequestSequence = useRef(0)
  const me = useQuery({ queryKey: ['me'], queryFn: getMe, retry: false })
  const models = useQuery({
    queryKey: ['models'],
    queryFn: () => api<{ revision: string; models: Model[] }>('/api/v1/models'),
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
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
    staleTime: Infinity,
  })
  const [modelID, setModelID] = useState('')
  const [prompt, setPrompt] = useState('')
  const [ratio, setRatio] = useState('1:1')
  const [resolution, setResolution] = useState('1K')
  const [quality, setQuality] = useState('auto')
  const [promptOptimizationMode, setPromptOptimizationMode] = useState<
    'standard' | 'fast'
  >('standard')
  const [draws, setDraws] = useState(1)
  const [midjourney, setMidjourney] = useState<MidjourneyOptions>({
    version: '8.2',
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
  const [references, setReferences] = useState<ReferenceItem[]>([])
  const [referenceSubmitBusy, setReferenceSubmitBusy] = useState(false)
  const [optimisticBatches, setOptimisticBatches] = useState<GenerationBatch[]>(
    [],
  )
  const [notice, setNotice] = useState('')
  const [referenceDropActive, setReferenceDropActive] = useState(false)
  const [uploadStates, setUploadStates] = useState<Record<string, string>>({})
  const [draftOwner, setDraftOwner] = useState('')
  const draftSnapshot = {
    modelID,
    prompt,
    ratio,
    resolution,
    quality,
    promptOptimizationMode,
    draws,
    midjourney,
    density,
    references: references.map((reference) =>
      reference.source === 'local'
        ? { ...reference, previewURL: '' }
        : reference,
    ),
  }
  const draftSnapshotRef = useRef(draftSnapshot)
  draftSnapshotRef.current = draftSnapshot
  const draftUserRef = useRef(me.data?.user.id)
  draftUserRef.current = me.data?.user.id
  const draftWriteTail = useRef(Promise.resolve())
  const saveDraft = useCallback(
    (owner: string, snapshot: typeof draftSnapshot) => {
      draftWriteTail.current = draftWriteTail.current
        .then(() => creationDraft(owner, snapshot))
        .then(() => undefined)
        .catch(() => {
          setNotice('浏览器草稿保存失败，请保留当前页面或复制描述')
        })
    },
    [],
  )
  useEffect(() => {
    const owner = me.data?.user.id
    if (!owner) return
    let disposed = false
    void creationDraft<typeof draftSnapshot>(owner)
      .then((draft) => {
        if (disposed) return
        if (draft) {
          setModelID(draft.modelID)
          setPrompt(draft.prompt)
          setRatio(draft.ratio)
          setResolution(draft.resolution)
          setQuality(draft.quality)
          setPromptOptimizationMode(draft.promptOptimizationMode)
          setDraws(draft.draws)
          setMidjourney(draft.midjourney)
          setDensity(draft.density)
          setReferences(
            draft.references.map((reference) => {
              if (reference.source !== 'local') return reference
              const previewURL = URL.createObjectURL(reference.file)
              localReferenceURLs.current.add(previewURL)
              return { ...reference, previewURL }
            }),
          )
        }
        setDraftOwner(owner)
      })
      .catch(() => {
        if (!disposed) {
          setDraftOwner(owner)
          setNotice('浏览器无法恢复草稿，本次仍可正常创作')
        }
      })
    return () => {
      disposed = true
    }
  }, [me.data?.user.id])
  useEffect(() => {
    if (!draftOwner || draftOwner !== me.data?.user.id) return
    const timer = window.setTimeout(
      () => saveDraft(draftOwner, draftSnapshotRef.current),
      500,
    )
    return () => window.clearTimeout(timer)
  }, [
    draftOwner,
    me.data?.user.id,
    modelID,
    prompt,
    ratio,
    resolution,
    quality,
    promptOptimizationMode,
    draws,
    midjourney,
    density,
    references,
    saveDraft,
  ])
  useEffect(() => {
    if (!draftOwner) return
    const flush = () => {
      if (draftUserRef.current === draftOwner)
        saveDraft(draftOwner, draftSnapshotRef.current)
    }
    window.addEventListener('pagehide', flush)
    return () => {
      window.removeEventListener('pagehide', flush)
      flush()
    }
  }, [draftOwner, saveDraft])

  useLayoutEffect(() => {
    resizePromptTextarea(promptRef.current)
  }, [prompt])

  async function editAsset(asset: Asset) {
    try {
      const project = await api<{ id: string }>(
        `/api/v1/assets/${asset.id}/editor-project`,
        { method: 'POST' },
      )
      sessionStorage.setItem('cornfield:editor:return', '/app/create')
      await navigate({
        to: '/app/editor/$projectId',
        params: { projectId: project.id },
      })
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '无法打开图片工作台')
    }
  }
  const [refinerBusy, setRefinerBusy] = useState(false)
  const [refinerReviewOpen, setRefinerReviewOpen] = useState(false)
  const [refinerUndo, setRefinerUndo] =
    useState<AppliedPromptRefinement | null>(null)
  const refinerUndoRef = useRef<AppliedPromptRefinement | null>(null)
  refinerUndoRef.current = refinerUndo
  const [confirm, setConfirm] = useState<{
    title: string
    description: string
    label: string
    dangerous?: boolean
    action: () => Promise<void>
  } | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const activeModel =
    models.data?.models.find((model) => model.id === modelID) ??
    models.data?.models[0]
  const maxDraws = activeModel?.capabilities.draw_count.max ?? 4
  const isMidjourney = activeModel?.id === 'legnext-midjourney'
  const midjourneyPromptLength = isMidjourney
    ? Array.from(prompt.trim()).length
    : 0
  const availableRatios =
    activeModel?.capabilities.aspect_ratios_by_resolution?.[resolution] ??
    activeModel?.capabilities.aspect_ratios ??
    []
  const refinerControlSignature = useMemo(
    () =>
      JSON.stringify({
        modelID: activeModel?.id,
        revision: models.data?.revision,
        ratio,
        resolution,
        quality,
        promptOptimizationMode,
        draws,
        midjourney,
        references: references.map((reference) => reference.key),
      }),
    [
      activeModel?.id,
      models.data?.revision,
      ratio,
      resolution,
      quality,
      promptOptimizationMode,
      draws,
      midjourney,
      references,
    ],
  )
  const refinerRequestSignature = `${refinerControlSignature}\n${prompt}`
  const refinerRequestSignatureRef = useRef(refinerRequestSignature)
  refinerRequestSignatureRef.current = refinerRequestSignature

  useEffect(() => {
    if (
      refinerAbort.current &&
      refinerPendingSignature.current !== refinerRequestSignature
    ) {
      refinerAbort.current.abort()
      refinerAbort.current = null
      refinerPendingSignature.current = ''
      setRefinerBusy(false)
    }
    if (
      refinerUndo &&
      (refinerUndo.after !== prompt ||
        refinerUndo.controlSignature !== refinerControlSignature)
    ) {
      setRefinerUndo(null)
      setRefinerReviewOpen(false)
    }
  }, [prompt, refinerControlSignature, refinerRequestSignature, refinerUndo])
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
      refinerAbort.current?.abort()
      refinerAbort.current = null
      for (const controller of uploadControllers.current) controller.abort()
      uploadControllers.current.clear()
      for (const url of localReferenceURLs.current) URL.revokeObjectURL(url)
      localReferenceURLs.current.clear()
    },
    [],
  )
  useEffect(() => {
    const activeURLs = new Set(
      references.flatMap((reference) =>
        reference.source === 'local' ? [reference.previewURL] : [],
      ),
    )
    for (const url of localReferenceURLs.current) {
      if (activeURLs.has(url)) continue
      URL.revokeObjectURL(url)
      localReferenceURLs.current.delete(url)
    }
  }, [references])
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
  const missingCompletedAssetRevision = useMemo(() => {
    if (!assets.isSuccess) return ''
    const cachedAssetIDs = new Set(
      assets.data.pages.flatMap((page) => page.items.map((asset) => asset.id)),
    )
    return (generations.data?.pages ?? [])
      .flatMap((page) => page.items)
      .filter((batch) => {
        if (batch.completed_outputs < 1) return false
        const outputAssetIDs = batch.jobs.flatMap((job) =>
          (job.outputs ?? []).map((output) => output.asset_id),
        )
        return (
          outputAssetIDs.length < batch.completed_outputs ||
          outputAssetIDs.some((id) => !cachedAssetIDs.has(id))
        )
      })
      .map((batch) => `${batch.id}:${batch.completed_outputs}`)
      .join('|')
  }, [assets.data?.pages, assets.isSuccess, generations.data?.pages])
  useEffect(() => {
    if (!missingCompletedAssetRevision) {
      assetRecoveryRevision.current = ''
      return
    }
    if (assetRecoveryRevision.current === missingCompletedAssetRevision) return
    assetRecoveryRevision.current = missingCompletedAssetRevision
    void refreshAssetHead()
  }, [missingCompletedAssetRevision, refreshAssetHead])
  useEffect(() => {
    if (!activeModel || draftOwner !== me.data?.user.id) return
    if (!modelID) setModelID(activeModel.id)
    if (!availableRatios.includes(ratio)) setRatio(availableRatios[0] ?? 'auto')
    if (!activeModel.capabilities.resolutions.includes(resolution))
      setResolution(activeModel.capabilities.resolutions[0] ?? 'auto')
    if (!(activeModel.capabilities.qualities ?? []).includes(quality))
      setQuality(activeModel.capabilities.qualities?.[0] ?? 'auto')
    if (
      !(activeModel.capabilities.prompt_optimization_modes ?? []).includes(
        promptOptimizationMode,
      )
    )
      setPromptOptimizationMode(
        activeModel.capabilities.prompt_optimization_modes?.[0] ?? 'standard',
      )
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
          (reference) =>
            referenceByteSize(reference) <=
            activeModel.capabilities.max_reference_bytes,
        )
        .slice(0, limit)
    })
  }, [
    draftOwner,
    me.data?.user.id,
    activeModel,
    availableRatios,
    modelID,
    promptOptimizationMode,
    quality,
    ratio,
    resolution,
  ])
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
    stream.onopen = () => {
      streamConnected.current = true
    }
    stream.onerror = () => {
      streamConnected.current = false
    }
    const reconcileTimers = new Map<string, number>()
    const reconcileAssets = new Set<string>()
    const reconcileInFlight = new Map<string, Promise<void>>()
    const reconcileBatch = (batchID: string) => {
      const existing = reconcileInFlight.get(batchID)
      if (existing) return existing
      const request = api<GenerationBatch>(`/api/v1/generations/${batchID}`)
        .then((batch) => mergeGenerationBatch(queryClient, batch))
        .then(() =>
          reconcileAssets.delete(batchID) ? refreshAssetHead() : undefined,
        )
        .catch(() => {
          void queryClient.invalidateQueries({ queryKey: ['generations'] })
        })
        .finally(() => reconcileInFlight.delete(batchID))
      reconcileInFlight.set(batchID, request)
      return request
    }
    const scheduleReconcile = (batchID: string, includeAssets: boolean) => {
      if (includeAssets) reconcileAssets.add(batchID)
      const existing = reconcileTimers.get(batchID)
      if (existing) window.clearTimeout(existing)
      reconcileTimers.set(
        batchID,
        window.setTimeout(() => {
          reconcileTimers.delete(batchID)
          void reconcileBatch(batchID)
        }, 2_000),
      )
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
      try {
        const envelope = JSON.parse(event.data) as JobEventEnvelope
        applyGenerationEvent(queryClient, envelope)
        if (envelope.batch_id) {
          scheduleReconcile(
            envelope.batch_id,
            envelope.type === 'job.succeeded',
          )
        }
      } catch {
        void queryClient.invalidateQueries({ queryKey: ['generations'] })
        void refreshAssetHead()
      }
    })
    stream.addEventListener('reset', (event) => {
      for (const timer of reconcileTimers.values()) window.clearTimeout(timer)
      reconcileTimers.clear()
      reconcileAssets.clear()
      updateCursor(event)
      void queryClient.invalidateQueries({ queryKey: ['generations'] })
      void refreshAssetHead()
    })
    return () => {
      for (const timer of reconcileTimers.values()) window.clearTimeout(timer)
      stream.close()
      streamConnected.current = false
    }
  }, [me.data?.user.id, queryClient, refreshAssetHead])
  useEffect(() => {
    if (!me.data?.user.id) return
    let busy = false
    let disposed = false
    const isDisposed = () => disposed
    let lastHead = 0
    const reconcile = async () => {
      if (busy || document.hidden) return
      busy = true
      try {
        if (!streamConnected.current || Date.now() - lastHead >= 60_000) {
          const head = await api<GenerationPage>(
            '/api/v1/generations?limit=100',
          )
          if (disposed) return
          for (const batch of head.items)
            mergeGenerationBatch(queryClient, batch)
          lastHead = Date.now()
        }
        const current = queryClient.getQueryData<GenerationPages>([
          'generations',
        ])
        const active = (
          current?.pages.flatMap((page) => page.items) ?? []
        ).filter((batch) =>
          batch.jobs.some(
            (job) =>
              ![
                'succeeded',
                'failed',
                'cancelled',
                'submission_uncertain',
              ].includes(job.status),
          ),
        )
        let index = 0
        await Promise.all(
          Array.from({ length: Math.min(3, active.length) }, async () => {
            while (index < active.length && !isDisposed()) {
              const batch = await api<GenerationBatch>(
                `/api/v1/generations/${active[index++].id}`,
              )
              if (!isDisposed()) mergeGenerationBatch(queryClient, batch)
            }
          }),
        )
      } catch {
        // The next bounded tick retries; historical pages are never refetched.
      } finally {
        busy = false
      }
    }
    const timer = window.setInterval(() => void reconcile(), 10_000)
    const onFocus = () => {
      lastHead = 0
      void reconcile()
      void refreshAssetHead()
    }
    window.addEventListener('focus', onFocus)
    return () => {
      disposed = true
      window.clearInterval(timer)
      window.removeEventListener('focus', onFocus)
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
      const refinementID = variables.refinementID
      if (refinementID) {
        reportPromptRefinementFeedback(refinementID, {
          event: 'submitted',
          batch_id: batch.id,
        })
        if (refinerUndoRef.current?.refinementID === refinementID) {
          setRefinerUndo(null)
          setRefinerReviewOpen(false)
        }
      }
      setNotice(`${batch.expected_outputs} 个生成位置已加入画布`)
    },
    onError: (reason, variables) => {
      if (
        reason instanceof APIError &&
        reason.code === 'PROVIDER_UNAVAILABLE'
      ) {
        void queryClient.invalidateQueries({ queryKey: ['models'] })
      }
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
  const dismissibleJobs = useMemo(
    () =>
      generationItems.flatMap((batch) =>
        batch.jobs
          .filter(
            (job) =>
              !job.dismissed_at &&
              dismissibleGenerationStatuses.has(job.status),
          )
          .map((job) => ({ batchID: batch.id, jobID: job.id })),
      ),
    [generationItems],
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
  function currentGenerationRequest(
    inputAssetIDs = uploadedReferenceIDs(references),
  ): GenerationRequest | null {
    if (!prompt.trim() || !activeModel || !models.data) return null
    const submittedResolution = isMidjourney
      ? midjourney.version === '8.2' ||
        midjourney.version === '8.1' ||
        midjourney.version === '8'
        ? (midjourney.resolution ?? 'sd').toUpperCase()
        : 'auto'
      : resolution
    const imageOptions = generationImageOptions(
      activeModel,
      quality,
      promptOptimizationMode,
    )
    return {
      model_id: activeModel.id,
      capability_revision: models.data.revision,
      prompt: prompt.trim(),
      aspect_ratio: ratio,
      resolution: submittedResolution,
      draw_count: draws,
      input_asset_ids: inputAssetIDs,
      options: isMidjourney ? { midjourney } : imageOptions,
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (referenceSubmitBusy || create.isPending) return
    const referenceSnapshot = [...references]
    const request = currentGenerationRequest(
      uploadedReferenceIDs(referenceSnapshot),
    )
    const modelSnapshot = activeModel
    if (!request || !modelSnapshot) return
    const appliedRefinementID = submittedRefinementID(
      refinerUndoRef.current,
      request.prompt,
    )

    const requestSignature = policyRetrySignature(request)
    const repeatedFailure = recentRepeatedPolicyFailure(
      request,
      generationItems,
    )
    if (repeatedFailure && repeatedPolicyBypass.current !== requestSignature) {
      repeatedPolicyBypass.current = requestSignature
      await restoreFailedBatch(repeatedFailure.id)
        .then(() =>
          setNotice('相同描述最近被安全策略拒绝，请先优化或修改后再生成'),
        )
        .catch((reason) =>
          setNotice(
            reason instanceof Error ? reason.message : '恢复失败任务失败',
          ),
        )
      return
    }

    setReferenceSubmitBusy(true)
    try {
      const inputAssetIDs = new Array<string>(referenceSnapshot.length)
      const deferred: Array<{
        reference: Extract<ReferenceItem, { source: 'local' }>
        index: number
      }> = []
      referenceSnapshot.forEach((reference, index) => {
        if (reference.source === 'asset') {
          inputAssetIDs[index] = reference.asset.id
        } else {
          deferred.push({ reference, index })
        }
      })
      let nextDeferred = 0
      let firstFailure: unknown
      await Promise.all(
        Array.from({ length: Math.min(3, deferred.length) }, async () => {
          while (nextDeferred < deferred.length) {
            const item = deferred[nextDeferred++]
            try {
              const asset = await uploadReferenceForGeneration(item.reference)
              inputAssetIDs[item.index] = asset.id
              setReferences((current) =>
                current.map((reference) =>
                  reference.key === item.reference.key
                    ? assetReference(asset)
                    : reference,
                ),
              )
            } catch (reason) {
              firstFailure ??= reason
            }
          }
        }),
      )
      if (firstFailure) throw firstFailure
      request.input_asset_ids = inputAssetIDs
    } catch (reason) {
      setNotice(reason instanceof Error ? reason.message : '参考图上传失败')
      return
    } finally {
      setReferenceSubmitBusy(false)
    }

    const idempotencyKey = crypto.randomUUID()
    const optimisticID = `optimistic:${idempotencyKey}`
    const createdAt = new Date().toISOString()
    const expectedOutputs = request.draw_count * modelSnapshot.outputs_per_draw
    const batch: GenerationBatch = {
      id: optimisticID,
      model_id: request.model_id,
      prompt: request.prompt,
      aspect_ratio: request.aspect_ratio,
      resolution: request.resolution,
      draw_count: request.draw_count,
      expected_outputs: expectedOutputs,
      completed_outputs: 0,
      status: 'queued',
      created_at: createdAt,
      jobs: Array.from({ length: request.draw_count }, (_, drawIndex) => ({
        id: `${optimisticID}:job:${drawIndex}`,
        draw_index: drawIndex,
        status: 'creating',
        expected_outputs: modelSnapshot.outputs_per_draw,
      })),
      options: request.options,
    }
    create.mutate({
      idempotencyKey,
      batch,
      refinementID: appliedRefinementID,
      request,
    })
  }

  async function requestPromptRefinement(
    request: GenerationRequest,
    signature: string,
    selection: { start: number; end: number },
    pendingReferenceCount = 0,
  ) {
    if (refinerBusyRef.current) return
    refinerBusyRef.current = true
    refinerAbort.current?.abort()
    const controller = new AbortController()
    const sequence = ++refinerRequestSequence.current
    refinerAbort.current = controller
    refinerPendingSignature.current = signature
    setRefinerBusy(true)
    try {
      const result = await api<PromptRefineResponse>('/api/v1/prompts/refine', {
        method: 'POST',
        signal: controller.signal,
        body: JSON.stringify({
          ...request,
          pending_reference_count: pendingReferenceCount,
        }),
      })
      if (
        controller.signal.aborted ||
        sequence !== refinerRequestSequence.current ||
        refinerRequestSignatureRef.current !== signature
      ) {
        return
      }
      const optimized = result.optimized_prompt
      if (!result.changed || !optimized || optimized === request.prompt) {
        setNotice(
          result.diagnostics?.[0]?.message ??
            (optimized === null
              ? '无法在保留原意的情况下安全优化，请手动调整'
              : '提示词已检查，无需修改'),
        )
        return
      }
      const nextSelection = mapRefinedSelection(
        request.prompt,
        optimized,
        selection,
      )
      setPrompt(optimized)
      setRefinerUndo({
        refinementID: result.refinement_id,
        before: request.prompt,
        after: optimized,
        selection,
        controlSignature: signature.endsWith(`\n${request.prompt}`)
          ? signature.slice(0, -request.prompt.length - 1)
          : refinerControlSignature,
      })
      setNotice('已优化提示词，可在生成前查看修改或撤销')
      requestAnimationFrame(() => {
        promptRef.current?.focus()
        promptRef.current?.setSelectionRange(
          nextSelection.start,
          nextSelection.end,
        )
      })
    } catch (reason) {
      if (!controller.signal.aborted)
        setNotice(reason instanceof Error ? reason.message : '提示词优化失败')
    } finally {
      if (refinerAbort.current === controller) {
        refinerAbort.current = null
        refinerPendingSignature.current = ''
        setRefinerBusy(false)
      }
      refinerBusyRef.current = false
    }
  }

  async function refinePrompt() {
    const request = currentGenerationRequest()
    if (!request || refinerBusy) return
    request.prompt = prompt
    await requestPromptRefinement(
      request,
      refinerRequestSignature,
      {
        start: promptRef.current?.selectionStart ?? prompt.length,
        end: promptRef.current?.selectionEnd ?? prompt.length,
      },
      references.filter((reference) => reference.source === 'local').length,
    )
  }
  function deleteAsset(asset: Asset) {
    setConfirm({
      title: '永久删除图片',
      description:
        '图片、缩略图及关联的未发布编辑工程将被永久删除，此操作无法撤销。',
      label: '确认删除',
      dangerous: true,
      action: () => performDeleteAsset(asset),
    })
  }
  async function performDeleteAsset(asset: Asset) {
    const previousAssets = await optimisticallyRemoveAssets(queryClient, [
      asset.id,
    ])
    const wasReference = references.some(
      (item) => item.source === 'asset' && item.asset.id === asset.id,
    )
    setReferences((current) =>
      current.filter(
        (item) => item.source !== 'asset' || item.asset.id !== asset.id,
      ),
    )
    try {
      await api(`/api/v1/assets/${asset.id}`, { method: 'DELETE' })
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['assets'] }),
        queryClient.invalidateQueries({ queryKey: ['generations'] }),
      ])
      setNotice('已从灵感墙移除，正在永久删除')
    } catch (reason) {
      restoreAssetCaches(queryClient, previousAssets)
      if (wasReference) {
        setReferences((current) =>
          current.some(
            (item) => item.source === 'asset' && item.asset.id === asset.id,
          )
            ? current
            : [...current, assetReference(asset)],
        )
      }
      setNotice(reason instanceof Error ? reason.message : '删除失败')
    }
  }
  function dismissJob(batchID: string, jobID: string) {
    setConfirm({
      title: '移除失败记录',
      description: '失败占位会从灵感墙移除，任务和上游审计仍会保留。',
      label: '确认移除',
      dangerous: true,
      action: () => performDismissJob(batchID, jobID),
    })
  }
  function dismissLoadedFailedJobs() {
    const jobs = dismissibleJobs.slice(0, 100)
    if (!jobs.length) return
    setConfirm({
      title: '清理失败项',
      description: `将从灵感墙移除 ${jobs.length} 个已结束的失败项，任务审计仍会保留。`,
      label: '确认清理',
      action: () => performDismissJobs(jobs),
    })
  }
  async function performDismissJobs(
    jobs: Array<{ batchID: string; jobID: string }>,
  ) {
    const previous = queryClient.getQueryData<GenerationPages>(['generations'])
    const jobIDs = new Set(jobs.map((job) => job.jobID))
    queryClient.setQueryData<GenerationPages>(['generations'], (current) => {
      if (!current) return current
      return {
        ...current,
        pages: current.pages.map((page) => ({
          ...page,
          items: page.items.map((batch) => ({
            ...batch,
            jobs: batch.jobs.map((job) =>
              jobIDs.has(job.id)
                ? { ...job, dismissed_at: new Date().toISOString() }
                : job,
            ),
          })),
        })),
      }
    })
    try {
      await api('/api/v1/generations/job-dismissals', {
        method: 'POST',
        body: JSON.stringify({
          jobs: jobs.map((job) => ({
            batch_id: job.batchID,
            job_id: job.jobID,
          })),
        }),
      })
      setNotice(
        dismissibleJobs.length > jobs.length
          ? `已清理 ${jobs.length} 个失败项，可继续清理剩余记录`
          : `已清理 ${jobs.length} 个失败项`,
      )
    } catch (reason) {
      queryClient.setQueryData(['generations'], previous)
      setNotice(reason instanceof Error ? reason.message : '清理失败项失败')
      throw reason
    }
  }
  async function performDismissJob(batchID: string, jobID: string) {
    const previous = queryClient.getQueryData<GenerationPages>(['generations'])
    queryClient.setQueryData<GenerationPages>(['generations'], (current) => {
      if (!current) return current
      return {
        ...current,
        pages: current.pages.map((page) => ({
          ...page,
          items: page.items.map((batch) =>
            batch.id !== batchID
              ? batch
              : {
                  ...batch,
                  jobs: batch.jobs.map((job) =>
                    job.id === jobID
                      ? { ...job, dismissed_at: new Date().toISOString() }
                      : job,
                  ),
                },
          ),
        })),
      }
    })
    try {
      await api(`/api/v1/generations/${batchID}/jobs/${jobID}`, {
        method: 'DELETE',
      })
      setNotice('失败记录已移除')
    } catch (reason) {
      queryClient.setQueryData(['generations'], previous)
      setNotice(reason instanceof Error ? reason.message : '移除失败')
    }
  }
  async function restoreFailedBatch(batchID: string, refine = false) {
    const batch = await api<GenerationBatch>(`/api/v1/generations/${batchID}`)
    setModelID(batch.model_id)
    setPrompt(batch.prompt)
    setRatio(batch.aspect_ratio)
    setResolution(batch.resolution)
    setDraws(batch.draw_count)
    if (batch.options?.midjourney) setMidjourney(batch.options.midjourney)
    if (batch.options?.image?.quality) setQuality(batch.options.image.quality)
    if (batch.options?.image?.prompt_optimization_mode)
      setPromptOptimizationMode(batch.options.image.prompt_optimization_mode)
    const restored = await Promise.all(
      (batch.input_asset_ids ?? []).map((id) =>
        api<Asset>(`/api/v1/assets/${id}`).catch(() => null),
      ),
    )
    setReferences(
      restored
        .filter((asset): asset is Asset => asset !== null)
        .map(assetReference),
    )
    await new Promise<void>((resolve) =>
      window.requestAnimationFrame(() => resolve()),
    )
    promptRef.current?.focus()
    if (refine && models.data) {
      const request: GenerationRequest = {
        model_id: batch.model_id,
        capability_revision: models.data.revision,
        prompt: batch.prompt,
        aspect_ratio: batch.aspect_ratio,
        resolution: batch.resolution,
        draw_count: batch.draw_count,
        input_asset_ids: batch.input_asset_ids ?? [],
        options: batch.options ?? {},
      }
      await requestPromptRefinement(
        request,
        refinerRequestSignatureRef.current,
        {
          start: batch.prompt.length,
          end: batch.prompt.length,
        },
      )
      return
    }
    setNotice('原参数已恢复，请调整描述或参数后重新生成')
  }
  function retryJob(batchID: string, jobID: string) {
    const batch = generationItems.find((item) => item.id === batchID)
    const job = batch?.jobs.find((item) => item.id === jobID)
    if (!batch || !job) return
    const action = failedJobAction(job)
    if (action === 'none') return
    if (action === 'edit') {
      void restoreFailedBatch(batchID).catch((error: Error) =>
        setNotice(error.message),
      )
      return
    }
    setConfirm({
      title: '重新提交生成',
      description: '这会创建一个新的上游任务，并可能产生新的费用。',
      label: '确认重试',
      action: async () => {
        await api(`/api/v1/generations/${batchID}/jobs/${jobID}/retry`, {
          method: 'POST',
        })
        await queryClient.invalidateQueries({ queryKey: ['generations'] })
        setNotice('已重新提交这一抽卡，其他结果不受影响')
      },
    })
  }
  function refineFailedJob(batchID: string, jobID: string) {
    if (refinerBusyRef.current) return
    const batch = generationItems.find((item) => item.id === batchID)
    const job = batch?.jobs.find((item) => item.id === jobID)
    if (!batch || !job || !canRefineGenerationError(job.error_code)) return
    void restoreFailedBatch(batchID, true).catch((error: Error) =>
      setNotice(error.message),
    )
  }
  async function runConfirmedAction() {
    if (!confirm || confirmBusy) return
    setConfirmBusy(true)
    try {
      await confirm.action()
      setConfirm(null)
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '操作失败')
    } finally {
      setConfirmBusy(false)
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
      current.some(
        (item) => item.source === 'asset' && item.asset.id === asset.id,
      )
        ? current
        : [...current, assetReference(asset)].slice(
            0,
            activeModel.capabilities.max_reference_images,
          ),
    )
    setNotice('已加入参考图')
  }
  async function uploadReferenceForGeneration(
    reference: Extract<ReferenceItem, { source: 'local' }>,
  ): Promise<Asset> {
    const controller = new AbortController()
    uploadControllers.current.add(controller)
    setUploadStates((current) => ({ ...current, [reference.key]: '上传中' }))
    try {
      const session = await api<{ id: string; content_url: string }>(
        '/api/v1/uploads',
        {
          method: 'POST',
          signal: controller.signal,
          body: JSON.stringify({
            filename: reference.file.name,
            media_type: reference.mediaType,
            size: reference.file.size,
            purpose: 'reference',
          }),
        },
      )
      await api(session.content_url, {
        method: 'PUT',
        body: reference.file,
        signal: controller.signal,
      })
      setUploadStates((current) => ({ ...current, [reference.key]: '验证中' }))
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
          throw new Error(referenceUploadErrorMessage(state.error_code))
        const remaining = deadline - Date.now()
        if (remaining <= 0) break
        await waitFor(Math.min(pollDelay, remaining), controller.signal)
        pollDelay = Math.min(Math.ceil(pollDelay * 1.5), 3_000)
      }
      if (!assetID) throw new Error('参考图仍在验证，请稍后重试')
      return await api<Asset>(`/api/v1/assets/${assetID}`, {
        signal: controller.signal,
      })
    } catch (error) {
      setUploadStates((current) => ({
        ...current,
        [reference.key]: '上传失败，生成时重试',
      }))
      throw error
    } finally {
      uploadControllers.current.delete(controller)
    }
  }

  function stageReferenceFiles(files: File[], retainedText = false) {
    const prefix = retainedText ? '文字已保留；' : ''
    if (!activeModel?.capabilities.image_to_image) {
      setNotice(`${prefix}当前模型不支持参考图`)
      return
    }
    const supported = files.flatMap((file) => {
      const mediaType = normalizeReferenceMediaType(file)
      if (!mediaType || file.size < 1) return []
      if (file.size > activeModel.capabilities.max_reference_bytes) return []
      return [{ file, mediaType }]
    })
    if (!supported.length) {
      setNotice(`${prefix}仅支持符合当前模型大小限制的 JPEG、PNG 或 WebP 图片`)
      return
    }
    const remaining = Math.max(
      0,
      activeModel.capabilities.max_reference_images - references.length,
    )
    if (!remaining) {
      setNotice(`${prefix}参考图数量已达到当前模型上限`)
      return
    }
    const selected = supported.slice(0, remaining)
    if (selected.length < supported.length)
      setNotice(`仅添加前 ${selected.length} 张图片，已达到参考图上限`)
    const staged = selected.map(({ file, mediaType }) => {
      const previewURL = URL.createObjectURL(file)
      localReferenceURLs.current.add(previewURL)
      return {
        key: `local:${crypto.randomUUID()}`,
        source: 'local' as const,
        file,
        previewURL,
        mediaType,
      }
    })
    setReferences((current) => [...current, ...staged])
    if (selected.length === supported.length)
      setNotice(`${prefix}已加入 ${selected.length} 张参考图，将在生成时上传`)
  }

  function pastePromptContent(event: ClipboardEvent<HTMLTextAreaElement>) {
    const images = clipboardImageFiles(event.clipboardData)
    if (!images.length) return

    const pastedText = event.clipboardData.getData('text/plain')
    event.preventDefault()
    if (pastedText) {
      const start = event.currentTarget.selectionStart
      const end = event.currentTarget.selectionEnd
      const nextPrompt = `${prompt.slice(0, start)}${pastedText}${prompt.slice(end)}`
      const nextCursor = start + pastedText.length
      setPrompt(nextPrompt)
      window.requestAnimationFrame(() => {
        promptRef.current?.focus()
        promptRef.current?.setSelectionRange(nextCursor, nextCursor)
      })
    }
    stageReferenceFiles(images, Boolean(pastedText))
  }

  function promptDragEnter(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    promptDragDepth.current += 1
    setReferenceDropActive(true)
  }

  function promptDragOver(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    event.dataTransfer.dropEffect = activeModel?.capabilities.image_to_image
      ? 'copy'
      : 'none'
  }

  function promptDragLeave(event: DragEvent<HTMLDivElement>) {
    if (promptDragDepth.current === 0) return
    event.preventDefault()
    promptDragDepth.current = Math.max(0, promptDragDepth.current - 1)
    if (promptDragDepth.current === 0) setReferenceDropActive(false)
  }

  function promptDrop(event: DragEvent<HTMLDivElement>) {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    promptDragDepth.current = 0
    setReferenceDropActive(false)
    stageReferenceFiles(Array.from(event.dataTransfer.files))
  }
  return (
    <AppShell>
      <main className="create-page">
        <div className="wall-toolbar">
          {dismissibleJobs.length > 0 && (
            <button
              className="failed-cleanup-button"
              type="button"
              onClick={dismissLoadedFailedJobs}
            >
              <Trash2 size={13} />
              清理失败项
              <span>{Math.min(dismissibleJobs.length, 100)}</span>
            </button>
          )}
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
          onEdit={(asset) => void editAsset(asset)}
          onDismiss={(batchID, jobID) => void dismissJob(batchID, jobID)}
          onRetry={retryJob}
          onRefine={refineFailedJob}
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
            {references.length > 0 && (
              <div className="reference-strip" aria-label="已选参考图">
                {references.map((reference) => (
                  <div
                    className="reference-card"
                    key={reference.key}
                    style={referencePreviewStyle(reference)}
                  >
                    <img
                      src={
                        reference.source === 'asset'
                          ? reference.asset.thumb_320_url
                          : reference.previewURL
                      }
                      alt="参考图"
                      onLoad={(event) => {
                        if (reference.source !== 'local') return
                        const { naturalWidth, naturalHeight } =
                          event.currentTarget
                        if (
                          naturalWidth < 1 ||
                          naturalHeight < 1 ||
                          (reference.width === naturalWidth &&
                            reference.height === naturalHeight)
                        )
                          return
                        setReferences((items) =>
                          items.map((item) =>
                            item.key === reference.key &&
                            item.source === 'local'
                              ? {
                                  ...item,
                                  width: naturalWidth,
                                  height: naturalHeight,
                                }
                              : item,
                          ),
                        )
                      }}
                    />
                    <button
                      type="button"
                      title="移除参考图"
                      disabled={referenceSubmitBusy}
                      aria-label="移除参考图"
                      onClick={() =>
                        setReferences((items) =>
                          items.filter((item) => item.key !== reference.key),
                        )
                      }
                    >
                      <X size={12} />
                    </button>
                    <span className="reference-upload-status" role="status">
                      {reference.source === 'asset'
                        ? '已就绪'
                        : (uploadStates[reference.key] ?? '待上传')}
                    </span>
                  </div>
                ))}
              </div>
            )}
            <div
              className="generator-prompt-row"
              onDragEnter={promptDragEnter}
              onDragOver={promptDragOver}
              onDragLeave={promptDragLeave}
              onDrop={promptDrop}
            >
              {referenceDropActive && (
                <div
                  className={`prompt-drop-overlay${activeModel?.capabilities.image_to_image ? '' : ' is-disabled'}`}
                  role="status"
                >
                  <span className="prompt-drop-mark" aria-hidden="true">
                    <Plus size={16} />
                  </span>
                  <span>
                    {activeModel?.capabilities.image_to_image
                      ? '松开，将图片置入参考区'
                      : '当前模型不支持参考图'}
                  </span>
                </div>
              )}
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
                  accept=".jpg,.jpeg,.png,.webp,image/jpeg,image/png,image/webp"
                  multiple
                  onChange={(event) => {
                    stageReferenceFiles(Array.from(event.target.files ?? []))
                    event.target.value = ''
                  }}
                />
              </label>
              <textarea
                disabled={!draftOwner || draftOwner !== me.data?.user.id}
                ref={promptRef}
                aria-label="生成提示词"
                value={prompt}
                onChange={(event) => {
                  setPrompt(event.target.value)
                  resizePromptTextarea(event.currentTarget)
                }}
                onPaste={pastePromptContent}
                placeholder="描述你想象中的画面"
                rows={1}
              />
              {isMidjourney && midjourneyPromptLength >= 900 && (
                <span
                  className={`midjourney-prompt-count${midjourneyPromptLength > 1024 ? ' is-over' : ''}`}
                  role="status"
                >
                  {midjourneyPromptLength}/1024
                </span>
              )}
              <button
                type="button"
                className={`prompt-refiner-button${refinerBusy ? ' is-busy' : ''}`}
                aria-label={refinerBusy ? '正在优化提示词' : '检查并优化提示词'}
                title={refinerBusy ? '正在优化提示词' : '检查并优化提示词'}
                disabled={!prompt.trim() || refinerBusy || !activeModel}
                onClick={() => void refinePrompt()}
              >
                <PromptRefinerIcon />
              </button>
            </div>
            <div className="generator-controls">
              {(activeModel?.estimated_wait?.upper_seconds ?? 0) > 0 && (
                <span className="generator-wait-estimate" role="status">
                  预计 {activeModel!.estimated_wait!.lower_seconds}–
                  {activeModel!.estimated_wait!.upper_seconds} 秒（历史估算）
                  {activeModel!.estimated_wait!.queued_draws > 0
                    ? ` · ${activeModel!.estimated_wait!.queued_draws} 次抽卡排队中`
                    : ''}
                </span>
              )}
              <GeneratorSelect
                label="选择模型"
                value={activeModel?.id ?? ''}
                items={(models.data?.models ?? []).map((model) => ({
                  value: model.id,
                  label: model.availability.can_submit
                    ? model.display_name
                    : `${model.display_name}（暂不可用）`,
                  disabled: !model.availability.can_submit,
                }))}
                icon={<Sparkles size={14} />}
                onChange={setModelID}
              />
              {!!availableRatios.length && (
                <GeneratorSelect
                  label="选择画面比例"
                  value={ratio}
                  items={availableRatios.map((item) => ({
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
              ) : (
                <>
                  {!!activeModel?.capabilities.resolutions.length && (
                    <GeneratorSelect
                      label="选择分辨率"
                      value={resolution}
                      items={activeModel.capabilities.resolutions.map(
                        (item) => ({ value: item, label: item }),
                      )}
                      icon={<span className="resolution-icon" />}
                      onChange={setResolution}
                    />
                  )}
                  {!!activeModel?.capabilities.qualities?.length && (
                    <GeneratorSelect
                      label="选择画质"
                      value={quality}
                      items={activeModel.capabilities.qualities.map((item) => ({
                        value: item,
                        label:
                          {
                            auto: '自动',
                            low: '低',
                            medium: '中',
                            high: '高',
                          }[item] ?? item,
                      }))}
                      icon={<span className="resolution-icon" />}
                      onChange={setQuality}
                    />
                  )}
                  {!!activeModel?.capabilities.prompt_optimization_modes
                    ?.length && (
                    <GeneratorSelect
                      label="选择提示词优化模式"
                      value={promptOptimizationMode}
                      items={activeModel.capabilities.prompt_optimization_modes.map(
                        (item) => ({
                          value: item,
                          label: item === 'fast' ? '快速' : '标准',
                        }),
                      )}
                      icon={<Sparkles size={14} />}
                      onChange={(value) =>
                        setPromptOptimizationMode(value as 'standard' | 'fast')
                      }
                    />
                  )}
                </>
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
            {activeModel && !activeModel.availability.can_submit && (
              <span className="generator-unavailable" role="status">
                {activeModel.availability.message ?? '生成服务暂不可用'}
              </span>
            )}
            {isMidjourney && midjourneyPromptLength > 1024 && (
              <span className="generator-unavailable" role="status">
                Midjourney 提示词过长，请精简后再生成
              </span>
            )}
          </div>
          <button
            className="generate-button"
            disabled={
              !draftOwner ||
              !prompt.trim() ||
              create.isPending ||
              refinerBusy ||
              referenceSubmitBusy ||
              midjourneyPromptLength > 1024 ||
              !activeModel?.availability.can_submit
            }
          >
            {referenceSubmitBusy
              ? `准备参考图 ${references.filter((reference) => reference.source === 'asset').length}/${references.length}`
              : create.isPending
                ? '提交中…'
                : '生成'}
          </button>
          {refinerUndo && (
            <div className="prompt-refiner-undo" role="status">
              <span>提示词已优化</span>
              <div className="prompt-refiner-undo-actions">
                <button
                  type="button"
                  className="is-review"
                  onClick={() => setRefinerReviewOpen(true)}
                >
                  查看修改
                </button>
                <button
                  type="button"
                  onClick={() => {
                    const undo = refinerUndo
                    reportPromptRefinementFeedback(undo.refinementID, {
                      event: 'undone',
                    })
                    setPrompt(undo.before)
                    setRefinerUndo(null)
                    setRefinerReviewOpen(false)
                    requestAnimationFrame(() => {
                      promptRef.current?.focus()
                      promptRef.current?.setSelectionRange(
                        undo.selection.start,
                        undo.selection.end,
                      )
                    })
                  }}
                >
                  撤销
                </button>
              </div>
            </div>
          )}
        </form>
        <PromptRefinerReview
          open={refinerReviewOpen && refinerUndo !== null}
          before={refinerUndo?.before ?? ''}
          after={refinerUndo?.after ?? ''}
          onClose={() => setRefinerReviewOpen(false)}
        />
        <ConfirmDialog
          open={confirm !== null}
          title={confirm?.title ?? ''}
          description={confirm?.description ?? ''}
          confirmLabel={confirm?.label}
          dangerous={confirm?.dangerous}
          busy={confirmBusy}
          onCancel={() => !confirmBusy && setConfirm(null)}
          onConfirm={() => void runConfirmedAction()}
        />
      </main>
    </AppShell>
  )
}
