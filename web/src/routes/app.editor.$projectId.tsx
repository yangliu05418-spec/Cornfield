import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  Crop,
  Eye,
  EyeOff,
  FlipHorizontal2,
  FlipVertical2,
  Layers3,
  Lock,
  Move,
  Maximize,
  Plus,
  Redo2,
  RotateCcw,
  Save,
  Sparkles,
  Trash2,
  Undo2,
  Unlock,
  X,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, PointerEvent as ReactPointerEvent } from 'react'

import { AppShell } from '#/components/app-shell'
import { ConfirmDialog } from '#/components/confirm-dialog'
import { api, APIError } from '#/lib/api'
import { mergeAssetIntoCaches } from '#/lib/asset-cache'
import {
  fitArtboard,
  flipAroundCenter,
  objectRotation,
  objectScale,
  rotateAroundCenter,
  scaleAroundCenter,
  transformPoint,
  zoomAtScreenPoint,
} from '#/lib/editor-transform'
import type {
  Asset,
  AssetOperation,
  EditorDocument,
  EditorObject,
  EditorProject,
  Model,
} from '#/lib/api'

export const Route = createFileRoute('/app/editor/$projectId')({
  component: ImageEditorPage,
})

type SaveState =
  'saved' | 'dirty' | 'saving' | 'offline' | 'conflict' | 'invalid'
type LayerSettings = {
  prompt: string
  resolution: 'auto' | '1K' | '1.5K' | '2K'
  mode: 'standard' | 'fast'
}

const terminalOperationStates = new Set([
  'succeeded',
  'failed',
  'cancelled',
  'submission_uncertain',
])
const maxEditorObjects = 64
const maxUploadBytes = 25 << 20
const acceptedUploadTypes = new Set(['image/jpeg', 'image/png', 'image/webp'])

function ImageEditorPage() {
  const { projectId } = Route.useParams()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const projectQuery = useQuery({
    queryKey: ['editor-project', projectId],
    queryFn: () => api<EditorProject>(`/api/v1/editor-projects/${projectId}`),
    retry: false,
  })
  const modelsQuery = useQuery({
    queryKey: ['models'],
    queryFn: () => api<{ revision: string; models: Model[] }>('/api/v1/models'),
    staleTime: 30_000,
  })
  const [documentState, setDocumentState] = useState<EditorDocument | null>(
    null,
  )
  const [selectedID, setSelectedID] = useState('')
  const [view, setView] = useState({ zoom: 100, panX: 0, panY: 0 })
  const [spacePressed, setSpacePressed] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [saveState, setSaveState] = useState<SaveState>('saved')
  const [notice, setNotice] = useState('')
  const [leaveConfirm, setLeaveConfirm] = useState(false)
  const [rerunConfirm, setRerunConfirm] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [settings, setSettings] = useState<LayerSettings>({
    prompt: '',
    resolution: 'auto',
    mode: 'standard',
  })
  const [operationID, setOperationID] = useState<string>()
  const [packageOperationID, setPackageOperationID] = useState<string>()
  const [elapsed, setElapsed] = useState(0)
  const revisionRef = useRef(0)
  const dirtyRef = useRef(false)
  const saveTimerRef = useRef<number | undefined>(undefined)
  const savePromiseRef = useRef<Promise<void> | null>(null)
  const documentRef = useRef<EditorDocument | null>(null)
  const historyRef = useRef<EditorDocument[]>([])
  const futureRef = useRef<EditorDocument[]>([])
  const appliedLayerSetRef = useRef<string | undefined>(undefined)
  const viewportRef = useRef<HTMLDivElement>(null)
  const uploadInputRef = useRef<HTMLInputElement>(null)
  const fittedRef = useRef(false)
  const zoom = view.zoom

  useEffect(() => {
    if (!projectQuery.data || documentRef.current) return
    documentRef.current = projectQuery.data.document
    setDocumentState(projectQuery.data.document)
    revisionRef.current = projectQuery.data.revision
    setSelectedID(projectQuery.data.document.objects.at(-1)?.id ?? '')
    setOperationID(projectQuery.data.latest_operation_id)
  }, [projectQuery.data])

  const fitCanvas = useCallback(() => {
    const viewport = viewportRef.current
    const document = documentRef.current
    if (!viewport || !document) return
    setView(
      fitArtboard(
        viewport.clientWidth,
        viewport.clientHeight,
        document.canvas.width,
        document.canvas.height,
      ),
    )
  }, [])

  useEffect(() => {
    if (!documentState || fittedRef.current) return
    fittedRef.current = true
    requestAnimationFrame(fitCanvas)
  }, [documentState, fitCanvas])

  useEffect(() => {
    const down = (event: KeyboardEvent) => {
      if (event.code === 'Space' && !isTypingTarget(event.target)) {
        event.preventDefault()
        setSpacePressed(true)
      }
    }
    const up = (event: KeyboardEvent) => {
      if (event.code === 'Space') setSpacePressed(false)
    }
    const blur = () => setSpacePressed(false)
    window.addEventListener('keydown', down)
    window.addEventListener('keyup', up)
    window.addEventListener('blur', blur)
    return () => {
      window.removeEventListener('keydown', down)
      window.removeEventListener('keyup', up)
      window.removeEventListener('blur', blur)
    }
  }, [])

  const assetIDs = useMemo(
    () => [
      ...new Set(documentState?.objects.map((item) => item.asset_id) ?? []),
    ],
    [documentState],
  )
  const assetsQuery = useQuery({
    queryKey: ['editor-assets', ...assetIDs],
    enabled: assetIDs.length > 0,
    queryFn: async () => {
      const assets = await Promise.all(
        assetIDs.map((id) => api<Asset>(`/api/v1/assets/${id}`)),
      )
      return new Map(assets.map((asset) => [asset.id, asset]))
    },
  })
  const operationQuery = useQuery({
    queryKey: ['asset-operation', operationID],
    enabled: Boolean(operationID),
    queryFn: () =>
      api<AssetOperation>(`/api/v1/asset-operations/${operationID}`),
    refetchInterval: (query) =>
      terminalOperationStates.has(query.state.data?.status ?? '')
        ? false
        : 2_000,
  })
  const operation = operationQuery.data
  const currentLayerSet =
    operation?.layer_set ?? projectQuery.data?.active_layer_set
  const packageOperationQuery = useQuery({
    queryKey: ['asset-operation', packageOperationID],
    enabled: Boolean(packageOperationID),
    queryFn: () =>
      api<AssetOperation>(`/api/v1/asset-operations/${packageOperationID}`),
    refetchInterval: (query) =>
      terminalOperationStates.has(query.state.data?.status ?? '')
        ? false
        : 2_000,
  })
  const operationRunning = Boolean(
    operationID && !terminalOperationStates.has(operation?.status ?? ''),
  )
  const layerCapability = modelsQuery.data?.models.find(
    (model) => model.id === 'byteplus-seedream-5-0-pro',
  )
  const canDecompose = Boolean(
    layerCapability?.availability.can_submit &&
    layerCapability.capabilities.layer_decomposition,
  )

  useEffect(() => {
    if (!operationRunning) {
      setElapsed(0)
      return
    }
    const started = Date.parse(
      operation?.started_at ?? operation?.created_at ?? '',
    )
    const startedAt = Number.isFinite(started) ? started : Date.now()
    const update = () =>
      setElapsed(Math.max(0, Math.floor((Date.now() - startedAt) / 1_000)))
    update()
    const timer = window.setInterval(update, 1_000)
    return () => window.clearInterval(timer)
  }, [operation?.created_at, operation?.started_at, operationRunning])

  const saveNow = useCallback(async () => {
    if (!dirtyRef.current || !documentRef.current) return
    if (savePromiseRef.current) return savePromiseRef.current
    const documentToSave = structuredClone(documentRef.current)
    const signature = JSON.stringify(documentToSave)
    setSaveState('saving')
    const task = saveEditorDocument(
      `/api/v1/editor-projects/${projectId}/document`,
      revisionRef.current,
      documentToSave,
    )
      .then((result) => {
        revisionRef.current = result.revision
        if (JSON.stringify(documentRef.current) === signature) {
          dirtyRef.current = false
          setSaveState('saved')
        } else {
          setSaveState('dirty')
          window.clearTimeout(saveTimerRef.current)
          saveTimerRef.current = window.setTimeout(
            () => void saveNow().catch(() => undefined),
            1_000,
          )
        }
      })
      .catch((error: unknown) => {
        if (error instanceof APIError && error.status === 409)
          setSaveState('conflict')
        else if (error instanceof APIError && error.status === 422)
          setSaveState('invalid')
        else setSaveState('offline')
        throw error
      })
      .finally(() => {
        savePromiseRef.current = null
      })
    savePromiseRef.current = task
    return task
  }, [projectId])

  const scheduleSave = useCallback(() => {
    dirtyRef.current = true
    setSaveState('dirty')
    window.clearTimeout(saveTimerRef.current)
    saveTimerRef.current = window.setTimeout(
      () => void saveNow().catch(() => undefined),
      1_000,
    )
  }, [saveNow])

  useEffect(
    () => () => {
      window.clearTimeout(saveTimerRef.current)
    },
    [],
  )

  function applyDocument(next: EditorDocument, remember = true) {
    if (!documentRef.current || operationRunning) return
    if (remember) {
      historyRef.current.push(structuredClone(documentRef.current))
      if (historyRef.current.length > 100) historyRef.current.shift()
      futureRef.current = []
    }
    documentRef.current = next
    setDocumentState(next)
    scheduleSave()
  }

  function updateObject(
    id: string,
    update: (object: EditorObject) => EditorObject,
  ) {
    if (!documentRef.current) return
    applyDocument({
      ...documentRef.current,
      objects: documentRef.current.objects.map((object) =>
        object.id === id ? update(object) : object,
      ),
    })
  }

  function undo() {
    if (!documentRef.current || !historyRef.current.length || operationRunning)
      return
    futureRef.current.push(structuredClone(documentRef.current))
    const previous = historyRef.current.pop()!
    documentRef.current = previous
    setDocumentState(previous)
    scheduleSave()
  }

  function redo() {
    if (!documentRef.current || !futureRef.current.length || operationRunning)
      return
    historyRef.current.push(structuredClone(documentRef.current))
    const next = futureRef.current.pop()!
    documentRef.current = next
    setDocumentState(next)
    scheduleSave()
  }

  async function leaveEditor() {
    try {
      await Promise.race([
        saveNow(),
        new Promise((_, reject) =>
          window.setTimeout(() => reject(new Error('save timeout')), 3_000),
        ),
      ])
    } catch {
      setLeaveConfirm(true)
      return
    }
    await returnToWorkspace()
  }

  async function returnToWorkspace() {
    const back =
      sessionStorage.getItem('cornfield:editor:return') || '/app/create'
    if (back === '/app/assets') await navigate({ to: '/app/assets' })
    else await navigate({ to: '/app/create' })
  }

  function downloadDocument() {
    if (!documentRef.current) return
    const blob = new Blob([JSON.stringify(documentRef.current, null, 2)], {
      type: 'application/json',
    })
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href
    link.download = `${projectQuery.data?.name || 'cornfield-editor'}.json`
    link.click()
    URL.revokeObjectURL(href)
  }

  function beginDrag(event: ReactPointerEvent, object: EditorObject) {
    if (
      spacePressed ||
      event.button === 1 ||
      object.locked ||
      operationRunning ||
      !documentRef.current
    )
      return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    const startX = event.clientX
    const startY = event.clientY
    const initialDocument = structuredClone(documentRef.current)
    const initial = [...object.transform] as EditorObject['transform']
    const move = (moveEvent: PointerEvent) => {
      const current = documentRef.current
      if (!current) return
      const next = {
        ...current,
        objects: current.objects.map((item) =>
          item.id === object.id
            ? {
                ...item,
                transform: [
                  initial[0],
                  initial[1],
                  initial[2],
                  initial[3],
                  initial[4] + (moveEvent.clientX - startX) / (zoom / 100),
                  initial[5] + (moveEvent.clientY - startY) / (zoom / 100),
                ],
              }
            : item,
        ),
      } satisfies EditorDocument
      documentRef.current = next
      setDocumentState(next)
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      historyRef.current.push(initialDocument)
      if (historyRef.current.length > 100) historyRef.current.shift()
      futureRef.current = []
      scheduleSave()
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
  }

  function beginPan(event: ReactPointerEvent<HTMLDivElement>) {
    if (!(spacePressed || event.button === 1)) return
    event.preventDefault()
    const startX = event.clientX
    const startY = event.clientY
    const initial = view
    const move = (moveEvent: PointerEvent) =>
      setView({
        ...initial,
        panX: initial.panX + moveEvent.clientX - startX,
        panY: initial.panY + moveEvent.clientY - startY,
      })
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
  }

  function beginObjectTransform(
    event: ReactPointerEvent<HTMLButtonElement>,
    object: EditorObject,
    asset: Asset,
    kind: 'scale' | 'rotate',
  ) {
    if (object.locked || operationRunning || !documentRef.current) return
    event.preventDefault()
    event.stopPropagation()
    const viewport = viewportRef.current
    if (!viewport) return
    const initialDocument = structuredClone(documentRef.current)
    const initialObject = structuredClone(object)
    const center = transformPoint(
      initialObject.transform,
      asset.width / 2,
      asset.height / 2,
    )
    const rect = viewport.getBoundingClientRect()
    const screenCenter = {
      x: rect.left + rect.width / 2 + view.panX + center.x * (view.zoom / 100),
      y: rect.top + rect.height / 2 + view.panY + center.y * (view.zoom / 100),
    }
    const startDistance = Math.max(
      1,
      Math.hypot(
        event.clientX - screenCenter.x,
        event.clientY - screenCenter.y,
      ),
    )
    const startAngle = Math.atan2(
      event.clientY - screenCenter.y,
      event.clientX - screenCenter.x,
    )
    const initialScale = objectScale(initialObject.transform)
    const move = (moveEvent: PointerEvent) => {
      const current = documentRef.current
      if (!current) return
      let transformed = initialObject
      if (kind === 'scale') {
        const distance = Math.hypot(
          moveEvent.clientX - screenCenter.x,
          moveEvent.clientY - screenCenter.y,
        )
        const target = Math.min(
          8,
          Math.max(0.05, initialScale * (distance / startDistance)),
        )
        transformed = scaleAroundCenter(
          initialObject,
          asset.width,
          asset.height,
          target,
        )
      } else {
        const angle = Math.atan2(
          moveEvent.clientY - screenCenter.y,
          moveEvent.clientX - screenCenter.x,
        )
        transformed = rotateAroundCenter(
          initialObject,
          asset.width,
          asset.height,
          ((angle - startAngle) * 180) / Math.PI,
        )
      }
      const next = {
        ...current,
        objects: current.objects.map((item) =>
          item.id === object.id ? transformed : item,
        ),
      }
      documentRef.current = next
      setDocumentState(next)
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      historyRef.current.push(initialDocument)
      if (historyRef.current.length > 100) historyRef.current.shift()
      futureRef.current = []
      scheduleSave()
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
  }

  function changeZoom(nextZoom: number) {
    const viewport = viewportRef.current
    if (!viewport) return
    const rect = viewport.getBoundingClientRect()
    setView((current) =>
      zoomAtScreenPoint(
        current,
        Math.min(400, Math.max(10, nextZoom)),
        rect.left + rect.width / 2,
        rect.top + rect.height / 2,
        rect,
      ),
    )
  }

  async function uploadEditorImage(file?: File) {
    if (!file || operationRunning || !documentRef.current) return
    if (!acceptedUploadTypes.has(file.type)) {
      setNotice('仅支持 JPEG、PNG 或 WebP 图片')
      return
    }
    if (file.size > maxUploadBytes) {
      setNotice('图片不能超过 25 MiB')
      return
    }
    if (documentRef.current.objects.length >= maxEditorObjects) {
      setNotice('当前工程最多可放置 64 个图层')
      return
    }
    setUploading(true)
    try {
      const session = await api<{ id: string; content_url: string }>(
        '/api/v1/uploads',
        {
          method: 'POST',
          body: JSON.stringify({
            filename: file.name,
            media_type: file.type,
            size: file.size,
          }),
        },
      )
      await api(session.content_url, { method: 'PUT', body: file })
      const deadline = Date.now() + 120_000
      let assetID = ''
      while (Date.now() < deadline) {
        const state = await api<{
          status: string
          asset_id?: string
          error_code?: string
        }>(`/api/v1/uploads/${session.id}`)
        if (state.status === 'ready' && state.asset_id) {
          assetID = state.asset_id
          break
        }
        if (state.status === 'failed')
          throw new Error(
            `图片验证失败：${state.error_code ?? 'IMAGE_INVALID'}`,
          )
        await new Promise((resolve) => window.setTimeout(resolve, 750))
      }
      if (!assetID) throw new Error('图片仍在验证，请稍后重试')
      const asset = await api<Asset>(`/api/v1/assets/${assetID}`)
      mergeAssetIntoCaches(queryClient, asset)
      const current = documentRef.current
      const scale = Math.min(
        1,
        (current.canvas.width * 0.72) / asset.width,
        (current.canvas.height * 0.72) / asset.height,
      )
      const nextObject: EditorObject = {
        id: crypto.randomUUID(),
        asset_id: asset.id,
        transform: [
          scale,
          0,
          0,
          scale,
          (current.canvas.width - asset.width * scale) / 2,
          (current.canvas.height - asset.height * scale) / 2,
        ],
        opacity: 1,
        visible: true,
        locked: false,
        z_index: current.objects.length,
      }
      applyDocument({ ...current, objects: [...current.objects, nextObject] })
      setSelectedID(nextObject.id)
      setNotice('图片已置入画板')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '图片上传失败')
    } finally {
      setUploading(false)
      if (uploadInputRef.current) uploadInputRef.current.value = ''
    }
  }

  async function startDecomposition(confirmed = false) {
    if (!canDecompose) {
      setNotice('智能分层仍在灰度验证中')
      return
    }
    if (currentLayerSet && !confirmed) {
      setRerunConfirm(true)
      return
    }
    try {
      await saveNow()
      const result = await api<{ id: string }>(
        `/api/v1/editor-projects/${projectId}/layer-decompositions`,
        {
          method: 'POST',
          headers: { 'Idempotency-Key': crypto.randomUUID() },
          body: JSON.stringify({
            expected_revision: revisionRef.current,
            prompt: settings.prompt,
            resolution: settings.resolution,
            prompt_optimization_mode: settings.mode,
          }),
        },
      )
      setOperationID(result.id)
      setSettingsOpen(false)
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '无法启动智能分层')
    }
  }

  function applyLayerSet() {
    const layerSet = operation?.layer_set
    if (!layerSet || !documentRef.current) return
    const objects: EditorObject[] = [
      {
        id: `base-${layerSet.id}`,
        asset_id: layerSet.base_asset.id,
        transform: [1, 0, 0, 1, 0, 0],
        opacity: 1,
        visible: true,
        locked: false,
        z_index: 0,
      },
      ...layerSet.items.map((item) => {
        const [left, top, right, bottom] = item.bounding_box_absolute
        return {
          id: item.id,
          asset_id: item.asset.id,
          transform: [
            (right - left) / item.asset.width,
            0,
            0,
            (bottom - top) / item.asset.height,
            left,
            top,
          ] as EditorObject['transform'],
          opacity: 1,
          visible: true,
          locked: false,
          z_index: item.z_index,
        }
      }),
    ]
    appliedLayerSetRef.current = layerSet.id
    applyDocument({ ...documentRef.current, objects })
    setSelectedID(objects.at(-1)?.id ?? '')
  }

  useEffect(() => {
    const layerSet = operation?.layer_set
    if (operation?.status === 'succeeded' && layerSet?.applied_to_project) {
      queryClient.setQueryData<EditorProject>(
        ['editor-project', projectId],
        (current) =>
          current
            ? {
                ...current,
                active_layer_set_id: layerSet.id,
                active_layer_set: layerSet,
              }
            : current,
      )
    }
    if (
      operation?.status === 'succeeded' &&
      layerSet?.applied_to_project &&
      appliedLayerSetRef.current !== layerSet.id &&
      documentRef.current &&
      revisionRef.current === operation.source_revision
    ) {
      applyLayerSet()
    }
    if (operation?.status === 'failed')
      setNotice(operation.error_message ?? '智能分层失败，请稍后重试')
  }, [
    operation?.id,
    operation?.status,
    operation?.layer_set?.id,
    projectId,
    queryClient,
  ])

  useEffect(() => {
    if (!operationID) return
    const source = new EventSource('/api/v1/events')
    let reconciliation: number | undefined
    const onOperationEvent = (event: MessageEvent) => {
      try {
        const envelope = JSON.parse(event.data) as {
          payload?: { id?: string; status?: string }
        }
        const payload = envelope.payload ?? {}
        if (payload.id !== operationID) return
        queryClient.setQueryData(
          ['asset-operation', operationID],
          (current: AssetOperation | undefined) =>
            current ? { ...current, ...payload } : current,
        )
        window.clearTimeout(reconciliation)
        reconciliation = window.setTimeout(
          () =>
            void queryClient.invalidateQueries({
              queryKey: ['asset-operation', operationID],
            }),
          2_000,
        )
      } catch {
        void queryClient.invalidateQueries({
          queryKey: ['asset-operation', operationID],
        })
      }
    }
    source.addEventListener('job', onOperationEvent)
    return () => {
      window.clearTimeout(reconciliation)
      source.removeEventListener('job', onOperationEvent)
      source.close()
    }
  }, [operationID, queryClient])

  async function publish() {
    try {
      await saveNow()
      const result = await api<{ id: string }>(
        `/api/v1/editor-projects/${projectId}/publish`,
        {
          method: 'POST',
          headers: { 'Idempotency-Key': crypto.randomUUID() },
          body: JSON.stringify({ expected_revision: revisionRef.current }),
        },
      )
      setOperationID(result.id)
      setNotice('正在保存为新图片')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '保存新图片失败')
    }
  }

  async function publishSelectedLayer() {
    if (!currentLayerSet || !selectedLayer) return
    try {
      const asset = await api<Asset>(
        `/api/v1/layer-sets/${currentLayerSet.id}/items/${selectedLayer.id}/publish`,
        { method: 'POST' },
      )
      mergeAssetIntoCaches(queryClient, asset)
      setNotice('图层已保存为新图片')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '保存图层失败')
    }
  }

  async function packageLayers() {
    const layerSet = currentLayerSet
    if (!layerSet) return
    if (layerSet.package_ready) {
      window.location.assign(
        `/api/v1/layer-sets/${layerSet.id}/package/content`,
      )
      return
    }
    try {
      const result = await api<{
        id?: string
        status: string
        content_url?: string
      }>(`/api/v1/layer-sets/${layerSet.id}/package`, {
        method: 'POST',
        headers: { 'Idempotency-Key': crypto.randomUUID() },
      })
      if (result.status === 'succeeded' && result.content_url) {
        window.location.assign(result.content_url)
        return
      }
      if (result.id) setPackageOperationID(result.id)
      setNotice('正在后台整理图层压缩包')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '创建压缩包失败')
    }
  }

  useEffect(() => {
    const result = packageOperationQuery.data
    if (!result || !currentLayerSet) return
    if (result.status === 'succeeded') {
      const layerSetID = currentLayerSet.id
      setPackageOperationID(undefined)
      window.location.assign(`/api/v1/layer-sets/${layerSetID}/package/content`)
    } else if (result.status === 'failed') {
      setNotice(result.error_message ?? '创建图层压缩包失败')
      setPackageOperationID(undefined)
    }
  }, [packageOperationQuery.data?.status, currentLayerSet?.id])

  useEffect(() => {
    if (
      operation?.operation_type !== 'editor_publish' ||
      operation.status !== 'succeeded' ||
      !operation.result_asset_id
    )
      return
    void api<Asset>(`/api/v1/assets/${operation.result_asset_id}`).then(
      (asset) => {
        mergeAssetIntoCaches(queryClient, asset)
        setNotice('新图片已置入灵感墙顶部')
      },
    )
  }, [
    operation?.id,
    operation?.status,
    operation?.result_asset_id,
    operation?.operation_type,
    queryClient,
  ])

  const selected = documentState?.objects.find(
    (object) => object.id === selectedID,
  )
  const selectedLayer = currentLayerSet?.items.find(
    (item) => item.id === selectedID,
  )
  const objectAssets = assetsQuery.data
  const selectedAsset = selected
    ? objectAssets?.get(selected.asset_id)
    : undefined

  function removeSelectedObject() {
    const current = documentRef.current
    if (!selected || !current || operationRunning) return
    if (selected.asset_id === projectQuery.data?.source_asset_id) {
      setNotice('源图是当前工程的锚点，可以隐藏，但不能移除')
      return
    }
    if (current.objects.length === 1) {
      setNotice('画板至少需要保留一个图层')
      return
    }
    const remaining = current.objects
      .filter((object) => object.id !== selected.id)
      .map((object, index) => ({ ...object, z_index: index }))
    applyDocument({ ...current, objects: remaining })
    setSelectedID(remaining.at(-1)?.id ?? '')
  }
  const sortedObjects = useMemo(
    () =>
      [...(documentState?.objects ?? [])].sort((a, b) => b.z_index - a.z_index),
    [documentState],
  )

  if (projectQuery.isLoading || !documentState)
    return (
      <AppShell immersive>
        <main className="editor-loading">
          <span className="spinner" />
          正在打开图片工作台
        </main>
      </AppShell>
    )
  if (projectQuery.isError)
    return (
      <AppShell immersive>
        <main className="editor-loading">图片编辑工程无法打开</main>
      </AppShell>
    )

  return (
    <AppShell immersive>
      <main className="image-editor">
        <header className="editor-topbar">
          <div className="editor-topbar-group">
            <button
              type="button"
              aria-label="返回上级界面"
              onClick={() => void leaveEditor()}
            >
              <ArrowLeft size={17} />
            </button>
            <input
              aria-label="工程名称"
              value={projectQuery.data?.name ?? ''}
              onChange={(event) =>
                queryClient.setQueryData<EditorProject>(
                  ['editor-project', projectId],
                  (current) =>
                    current
                      ? { ...current, name: event.target.value }
                      : current,
                )
              }
              onBlur={(event) => {
                const name = event.target.value.trim()
                if (name)
                  void api(`/api/v1/editor-projects/${projectId}`, {
                    method: 'PATCH',
                    body: JSON.stringify({ name }),
                  })
              }}
            />
            <span className={`editor-save-state is-${saveState}`}>
              {saveStateLabel(saveState)}
            </span>
          </div>
          <div className="editor-topbar-group editor-history">
            <button
              type="button"
              aria-label="撤销"
              disabled={!historyRef.current.length || operationRunning}
              onClick={undo}
            >
              <Undo2 size={16} />
            </button>
            <button
              type="button"
              aria-label="重做"
              disabled={!futureRef.current.length || operationRunning}
              onClick={redo}
            >
              <Redo2 size={16} />
            </button>
          </div>
          <div className="editor-topbar-group editor-primary-actions">
            <button
              type="button"
              className="editor-layer-settings"
              aria-label="智能分层设置"
              onClick={() => setSettingsOpen((value) => !value)}
            >
              <Layers3 size={16} />
            </button>
            <button
              type="button"
              className="editor-decompose"
              disabled={operationRunning || !canDecompose}
              title={
                canDecompose
                  ? '智能分层'
                  : layerCapability?.availability.message || '智能分层暂未开放'
              }
              onClick={() => void startDecomposition()}
            >
              <Sparkles size={16} />
              智能分层
            </button>
            <button
              type="button"
              className="editor-publish"
              disabled={operationRunning}
              onClick={() => void publish()}
            >
              <Save size={16} />
              保存为新图片
            </button>
          </div>
        </header>

        <section className="editor-body">
          <aside className="editor-tools" aria-label="基础编辑工具">
            <input
              ref={uploadInputRef}
              className="sr-only"
              type="file"
              accept="image/jpeg,image/png,image/webp"
              onChange={(event) =>
                void uploadEditorImage(event.currentTarget.files?.[0])
              }
            />
            <button
              type="button"
              title="添加图片"
              aria-label="添加图片"
              disabled={uploading || operationRunning}
              onClick={() => uploadInputRef.current?.click()}
            >
              <Plus size={17} />
            </button>
            <button type="button" title="适应画板" onClick={fitCanvas}>
              <Maximize size={17} />
            </button>
            <button
              type="button"
              title="按住空格拖动画布"
              aria-label="画布平移工具"
              className={spacePressed ? 'active' : ''}
              onClick={() => setSpacePressed((current) => !current)}
            >
              <Move size={17} />
            </button>
            <button
              type="button"
              title="旋转 90°"
              disabled={!selected || operationRunning}
              onClick={() =>
                selected &&
                selectedAsset &&
                updateObject(selected.id, (object) =>
                  rotateAroundCenter(
                    object,
                    selectedAsset.width,
                    selectedAsset.height,
                    90,
                  ),
                )
              }
            >
              <RotateCcw size={17} />
            </button>
            <button
              type="button"
              title="水平翻转"
              disabled={!selected || operationRunning}
              onClick={() =>
                selected &&
                selectedAsset &&
                updateObject(selected.id, (object) =>
                  flipAroundCenter(
                    object,
                    selectedAsset.width,
                    selectedAsset.height,
                    'horizontal',
                  ),
                )
              }
            >
              <FlipHorizontal2 size={17} />
            </button>
            <button
              type="button"
              title="垂直翻转"
              disabled={!selected || operationRunning}
              onClick={() =>
                selected &&
                selectedAsset &&
                updateObject(selected.id, (object) =>
                  flipAroundCenter(
                    object,
                    selectedAsset.width,
                    selectedAsset.height,
                    'vertical',
                  ),
                )
              }
            >
              <FlipVertical2 size={17} />
            </button>
            <button
              type="button"
              title="裁切边缘"
              disabled={!selected || operationRunning}
              onClick={() =>
                selected &&
                updateObject(selected.id, (object) => ({
                  ...object,
                  crop: object.crop
                    ? undefined
                    : { x: 0.05, y: 0.05, width: 0.9, height: 0.9 },
                }))
              }
            >
              <Crop size={17} />
            </button>
          </aside>

          <div
            ref={viewportRef}
            className="editor-canvas-viewport"
            role="region"
            tabIndex={0}
            aria-label="图片编辑画布"
            data-panning={spacePressed || undefined}
            onPointerDown={beginPan}
            onWheel={(event) => {
              if (!(event.ctrlKey || event.metaKey)) return
              event.preventDefault()
              const rect = event.currentTarget.getBoundingClientRect()
              const nextZoom = Math.min(
                400,
                Math.max(10, view.zoom * Math.exp(-event.deltaY * 0.002)),
              )
              setView((current) =>
                zoomAtScreenPoint(
                  current,
                  nextZoom,
                  event.clientX,
                  event.clientY,
                  rect,
                ),
              )
            }}
            onKeyDown={(event) => {
              if (
                selected &&
                !selected.locked &&
                !operationRunning &&
                (event.key === 'Delete' || event.key === 'Backspace')
              ) {
                event.preventDefault()
                removeSelectedObject()
                return
              }
              if (
                !selected ||
                selected.locked ||
                operationRunning ||
                !['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(
                  event.key,
                )
              )
                return
              event.preventDefault()
              const distance = event.shiftKey ? 10 : 1
              const dx =
                event.key === 'ArrowLeft'
                  ? -distance
                  : event.key === 'ArrowRight'
                    ? distance
                    : 0
              const dy =
                event.key === 'ArrowUp'
                  ? -distance
                  : event.key === 'ArrowDown'
                    ? distance
                    : 0
              updateObject(selected.id, (object) => ({
                ...object,
                transform: [
                  object.transform[0],
                  object.transform[1],
                  object.transform[2],
                  object.transform[3],
                  object.transform[4] + dx,
                  object.transform[5] + dy,
                ],
              }))
            }}
          >
            <div
              className="editor-world"
              style={{
                transform: `translate(${view.panX}px, ${view.panY}px) scale(${zoom / 100})`,
              }}
            >
              <span className="editor-artboard-label">
                画板 · {documentState.canvas.width} ×{' '}
                {documentState.canvas.height}
              </span>
              <div
                className="editor-canvas editor-artboard"
                style={
                  {
                    width: documentState.canvas.width,
                    height: documentState.canvas.height,
                    '--editor-zoom': zoom / 100,
                  } as CSSProperties
                }
              >
                {documentState.objects.map((object) => {
                  const asset = objectAssets?.get(object.asset_id)
                  if (!asset || !object.visible) return null
                  return (
                    <img
                      key={object.id}
                      className={object.id === selectedID ? 'is-selected' : ''}
                      src={asset.thumb_1280_url || asset.thumb_640_url}
                      width={asset.width}
                      height={asset.height}
                      draggable={false}
                      alt="画布图层"
                      style={{
                        opacity: object.opacity,
                        zIndex: object.z_index,
                        transform: `matrix(${object.transform.join(',')})`,
                        clipPath: object.crop
                          ? `inset(${object.crop.y * 100}% ${(1 - object.crop.x - object.crop.width) * 100}% ${(1 - object.crop.y - object.crop.height) * 100}% ${object.crop.x * 100}%)`
                          : undefined,
                      }}
                      onPointerDown={(event) => {
                        setSelectedID(object.id)
                        if (spacePressed || event.button === 1) return
                        beginDrag(event, object)
                      }}
                    />
                  )
                })}
              </div>
              {selected && selectedAsset && selected.visible && (
                <div
                  className="editor-selection-box"
                  style={
                    {
                      width: selectedAsset.width,
                      height: selectedAsset.height,
                      transform: `matrix(${selected.transform.join(',')})`,
                      '--editor-handle-size': `${12 / (zoom / 100) / Math.max(0.05, objectScale(selected.transform))}px`,
                      '--editor-handle-distance': `${34 / (zoom / 100) / Math.max(0.05, objectScale(selected.transform))}px`,
                    } as CSSProperties
                  }
                  aria-hidden={selected.locked || operationRunning}
                >
                  {!selected.locked && !operationRunning && (
                    <>
                      {['nw', 'ne', 'se', 'sw'].map((position) => (
                        <button
                          key={position}
                          className={`editor-transform-handle is-${position}`}
                          type="button"
                          aria-label="缩放图层"
                          onPointerDown={(event) =>
                            beginObjectTransform(
                              event,
                              selected,
                              selectedAsset,
                              'scale',
                            )
                          }
                        />
                      ))}
                      <button
                        className="editor-rotate-handle"
                        type="button"
                        aria-label="旋转图层"
                        onPointerDown={(event) =>
                          beginObjectTransform(
                            event,
                            selected,
                            selectedAsset,
                            'rotate',
                          )
                        }
                      />
                    </>
                  )}
                </div>
              )}
            </div>
            {operationRunning && (
              <DecompositionWaiting
                status={operation?.status}
                message={operation?.message}
                elapsed={elapsed}
              />
            )}
            {operation?.status === 'succeeded' &&
              operation.layer_set &&
              !operation.layer_set.applied_to_project && (
                <button
                  className="editor-pending-result"
                  type="button"
                  onClick={applyLayerSet}
                >
                  工程已在其他标签页更新 · 应用这次分层结果
                </button>
              )}
          </div>

          <aside className="editor-inspector">
            <div className="editor-panel-tabs">
              <strong>图层</strong>
              <span>{documentState.objects.length} / 64</span>
            </div>
            <div className="editor-layer-list">
              {sortedObjects.map((object) => (
                <button
                  key={object.id}
                  type="button"
                  className={object.id === selectedID ? 'active' : ''}
                  onClick={() => setSelectedID(object.id)}
                >
                  <img
                    src={objectAssets?.get(object.asset_id)?.thumb_320_url}
                    alt=""
                  />
                  <span>
                    {currentLayerSet?.items.find(
                      (item) => item.id === object.id,
                    )?.name ??
                      (object.z_index === 0
                        ? '背景'
                        : `图层 ${object.z_index}`)}
                  </span>
                  <i>
                    {object.visible ? <Eye size={13} /> : <EyeOff size={13} />}
                  </i>
                </button>
              ))}
            </div>
            {selected && (
              <div className="editor-properties">
                <label>
                  等比缩放{' '}
                  <output>
                    {Math.round(objectScale(selected.transform) * 100)}%
                  </output>
                  <input
                    aria-label="图层缩放"
                    type="range"
                    min="0.1"
                    max="4"
                    step="0.01"
                    value={objectScale(selected.transform)}
                    disabled={operationRunning || selected.locked}
                    onChange={(event) => {
                      if (!selectedAsset) return
                      updateObject(selected.id, (object) =>
                        scaleAroundCenter(
                          object,
                          selectedAsset.width,
                          selectedAsset.height,
                          Number(event.target.value),
                        ),
                      )
                    }}
                  />
                </label>
                <button
                  className="editor-remove-layer"
                  type="button"
                  disabled={
                    operationRunning ||
                    documentState.objects.length <= 1 ||
                    selected.asset_id === projectQuery.data?.source_asset_id
                  }
                  onClick={removeSelectedObject}
                >
                  <Trash2 size={15} />
                  移除图层
                </button>
                <label>
                  旋转{' '}
                  <output>
                    {Math.round(objectRotation(selected.transform))}°
                  </output>
                  <input
                    aria-label="图层旋转"
                    type="range"
                    min="-180"
                    max="180"
                    step="1"
                    value={objectRotation(selected.transform)}
                    disabled={operationRunning || selected.locked}
                    onChange={(event) => {
                      if (!selectedAsset) return
                      updateObject(selected.id, (object) =>
                        rotateAroundCenter(
                          object,
                          selectedAsset.width,
                          selectedAsset.height,
                          Number(event.target.value) -
                            objectRotation(object.transform),
                        ),
                      )
                    }}
                  />
                </label>
                <label>
                  透明度 <output>{Math.round(selected.opacity * 100)}%</output>
                  <input
                    type="range"
                    min="0"
                    max="1"
                    step="0.01"
                    value={selected.opacity}
                    disabled={operationRunning}
                    onChange={(event) =>
                      updateObject(selected.id, (object) => ({
                        ...object,
                        opacity: Number(event.target.value),
                      }))
                    }
                  />
                </label>
                {selected.crop && (
                  <label>
                    裁切边距{' '}
                    <output>{Math.round(selected.crop.x * 100)}%</output>
                    <input
                      aria-label="图层裁切边距"
                      type="range"
                      min="0"
                      max="0.45"
                      step="0.01"
                      value={selected.crop.x}
                      disabled={operationRunning || selected.locked}
                      onChange={(event) => {
                        const inset = Number(event.target.value)
                        updateObject(selected.id, (object) => ({
                          ...object,
                          crop: {
                            x: inset,
                            y: inset,
                            width: 1 - inset * 2,
                            height: 1 - inset * 2,
                          },
                        }))
                      }}
                    />
                  </label>
                )}
                <div className="editor-property-actions">
                  <button
                    type="button"
                    onClick={() =>
                      updateObject(selected.id, (object) => ({
                        ...object,
                        visible: !object.visible,
                      }))
                    }
                  >
                    {selected.visible ? (
                      <Eye size={14} />
                    ) : (
                      <EyeOff size={14} />
                    )}
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      updateObject(selected.id, (object) => ({
                        ...object,
                        locked: !object.locked,
                      }))
                    }
                  >
                    {selected.locked ? (
                      <Lock size={14} />
                    ) : (
                      <Unlock size={14} />
                    )}
                  </button>
                  <button
                    type="button"
                    disabled={
                      selected.z_index >= documentState.objects.length - 1
                    }
                    onClick={() =>
                      changeLayerOrder(
                        selected.id,
                        1,
                        documentRef,
                        applyDocument,
                      )
                    }
                  >
                    <ArrowUp size={14} />
                  </button>
                  <button
                    type="button"
                    disabled={selected.z_index <= 0}
                    onClick={() =>
                      changeLayerOrder(
                        selected.id,
                        -1,
                        documentRef,
                        applyDocument,
                      )
                    }
                  >
                    <ArrowDown size={14} />
                  </button>
                </div>
                {selectedLayer && (
                  <div className="editor-layer-output-actions">
                    <a href={`${selectedLayer.asset.url}?download=1`}>
                      下载图层
                    </a>
                    <button
                      type="button"
                      onClick={() => void publishSelectedLayer()}
                    >
                      保存为图片
                    </button>
                  </div>
                )}
                {currentLayerSet && (
                  <button
                    className="editor-package-layers"
                    type="button"
                    disabled={Boolean(packageOperationID)}
                    onClick={() => void packageLayers()}
                  >
                    {packageOperationID ? '正在打包' : '下载全部图层'}
                  </button>
                )}
              </div>
            )}
          </aside>
        </section>

        <footer className="editor-statusbar">
          <span>
            {documentState.canvas.width} × {documentState.canvas.height}
          </span>
          <div>
            <button type="button" onClick={() => changeZoom(50)}>
              50%
            </button>
            <button type="button" onClick={() => changeZoom(100)}>
              100%
            </button>
            <button type="button" onClick={() => changeZoom(200)}>
              200%
            </button>
            <input
              aria-label="画布缩放"
              type="range"
              min="10"
              max="400"
              value={zoom}
              onChange={(event) => changeZoom(Number(event.target.value))}
            />
          </div>
        </footer>

        {(saveState === 'offline' ||
          saveState === 'conflict' ||
          saveState === 'invalid') && (
          <aside className="editor-save-recovery" role="alert">
            <strong>
              {saveState === 'conflict'
                ? '工程已在其他页面更新'
                : saveState === 'invalid'
                  ? '自动保存已暂停'
                  : '当前修改尚未保存'}
            </strong>
            <span>本地编辑仍在当前页面中，可以先下载工程 JSON。</span>
            <div>
              {saveState === 'offline' && (
                <button type="button" onClick={() => void saveNow()}>
                  重新保存
                </button>
              )}
              <button type="button" onClick={downloadDocument}>
                下载工程 JSON
              </button>
            </div>
          </aside>
        )}

        {settingsOpen && (
          <aside className="editor-layer-drawer">
            <button
              className="drawer-close"
              aria-label="关闭智能分层设置"
              onClick={() => setSettingsOpen(false)}
            >
              <X size={16} />
            </button>
            <p className="eyebrow">INTELLIGENT LAYERS</p>
            <h2>智能分层设置</h2>
            <label>
              指定元素
              <textarea
                value={settings.prompt}
                maxLength={8192}
                placeholder="可选，例如：分离人物、前景植物与标题文字"
                onChange={(event) =>
                  setSettings((current) => ({
                    ...current,
                    prompt: event.target.value,
                  }))
                }
              />
            </label>
            <label>
              输出尺寸
              <select
                value={settings.resolution}
                onChange={(event) =>
                  setSettings((current) => ({
                    ...current,
                    resolution: event.target
                      .value as LayerSettings['resolution'],
                  }))
                }
              >
                <option value="auto">自动</option>
                <option value="1K">1K</option>
                <option value="1.5K">1.5K</option>
                <option value="2K">2K</option>
              </select>
            </label>
            <label>
              处理模式
              <select
                value={settings.mode}
                onChange={(event) =>
                  setSettings((current) => ({
                    ...current,
                    mode: event.target.value as LayerSettings['mode'],
                  }))
                }
              >
                <option value="standard">标准 · 更稳定</option>
                <option value="fast">快速 · 更低延迟</option>
              </select>
            </label>
            <p>
              首次分层将按实际返回图层数量计费。重新分层不会覆盖旧结果，直到新结果成功。
            </p>
            <button
              className="editor-decompose"
              type="button"
              onClick={() => void startDecomposition()}
            >
              <Sparkles size={16} />
              开始智能分层
            </button>
          </aside>
        )}
        {notice && (
          <div className="notice" role="status">
            <span>{notice}</span>
            <button aria-label="关闭提示" onClick={() => setNotice('')}>
              <X size={12} />
            </button>
          </div>
        )}
        <ConfirmDialog
          open={rerunConfirm}
          title="重新智能分层"
          description="这会创建一次新的付费处理。旧图层会保留到新结果成功，不会被提前覆盖。"
          confirmLabel="确认重新分层"
          onCancel={() => setRerunConfirm(false)}
          onConfirm={() => {
            setRerunConfirm(false)
            void startDecomposition(true)
          }}
        />
        <ConfirmDialog
          open={leaveConfirm}
          title="修改尚未保存"
          description="可以继续留在工作台重试保存，或放弃本次未保存修改并返回工作区。"
          confirmLabel="仍然返回"
          dangerous
          onCancel={() => setLeaveConfirm(false)}
          onConfirm={() => {
            setLeaveConfirm(false)
            void returnToWorkspace()
          }}
        />
      </main>
    </AppShell>
  )
}

function DecompositionWaiting({
  status,
  message,
  elapsed,
}: {
  status?: string
  message?: string
  elapsed: number
}) {
  const messages: Record<string, string> = {
    queued: '等待处理资源',
    dispatched: '等待处理资源',
    snapshotting: '识别画面结构',
    submitting: '区分主体与背景',
    provider_processing: '整理图层关系',
    ingesting: '生成图层预览',
  }
  return (
    <div className="decomposition-wait" aria-live="polite">
      <div className="decomposition-scan" />
      <Sparkles size={24} />
      <strong>{message || messages[status ?? ''] || '准备透明图层'}</strong>
      <span>{elapsed} 秒 · 可以返回，任务会在后台继续</span>
    </div>
  )
}

async function saveEditorDocument(
  url: string,
  expectedRevision: number,
  document: EditorDocument,
): Promise<{ revision: number }> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await api<{ revision: number }>(url, {
        method: 'PUT',
        body: JSON.stringify({
          expected_revision: expectedRevision,
          document,
        }),
      })
    } catch (error) {
      const retryable =
        error instanceof TypeError ||
        (error instanceof APIError &&
          (error.status === 429 || error.status >= 500))
      if (!retryable || attempt >= 2) throw error
      await new Promise((resolve) =>
        window.setTimeout(resolve, 400 * 2 ** attempt),
      )
    }
  }
}

function isTypingTarget(target: EventTarget | null) {
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    (target instanceof HTMLElement && target.isContentEditable)
  )
}

function changeLayerOrder(
  id: string,
  direction: -1 | 1,
  documentRef: { current: EditorDocument | null },
  apply: (document: EditorDocument) => void,
) {
  const current = documentRef.current
  if (!current) return
  const target = current.objects.find((object) => object.id === id)
  const sibling = current.objects.find(
    (object) => object.z_index === (target?.z_index ?? 0) + direction,
  )
  if (!target || !sibling) return
  apply({
    ...current,
    objects: current.objects.map((object) =>
      object.id === target.id
        ? { ...object, z_index: sibling.z_index }
        : object.id === sibling.id
          ? { ...object, z_index: target.z_index }
          : object,
    ),
  })
}

function saveStateLabel(state: SaveState) {
  return {
    saved: '已保存',
    dirty: '等待保存',
    saving: '保存中',
    offline: '保存失败',
    conflict: '版本冲突',
    invalid: '自动保存已暂停',
  }[state]
}
