import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlignCenterHorizontal,
  AlignCenterVertical,
  AlignEndHorizontal,
  AlignEndVertical,
  AlignHorizontalDistributeCenter,
  AlignStartHorizontal,
  AlignStartVertical,
  AlignVerticalDistributeCenter,
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  Check,
  Crop,
  Copy,
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
import { StructuredEditor } from '#/features/editor/structured-editor'
import { EditorOperationWaiting } from '#/features/editor/editor-operation-waiting'
import { EditorHistory } from '#/features/editor/domain/history'
import {
  applyFlatEditorViewToV2,
  projectFlatEditorDocumentV2,
} from '#/features/editor/domain/flat-authoring-v2'
import { migrateEditorDocumentV1ToV2 } from '#/features/editor/domain/document-v2'
import type { EditorDocumentV2 } from '#/features/editor/domain/document-v2'
import { PixiSurface } from '#/features/editor/renderer/pixi-surface'
import { useEditorOperations } from '#/features/editor/use-editor-operations'
import type { LayerDecompositionSettings } from '#/features/editor/use-editor-operations'
import { api, APIError } from '#/lib/api'
import { mergeAssetIntoCaches } from '#/lib/asset-cache'
import {
  fitArtboard,
  flipAroundCenter,
  alignmentOffset,
  distributionOffsets,
  invertAffine,
  moveCrop,
  objectRotation,
  objectScale,
  objectBounds,
  objectAxisScales,
  moveObjectCenter,
  rotateAroundCenter,
  rotateAroundWorldPoint,
  resizeCrop,
  scaleAroundCenter,
  scaleByFactorAroundCenter,
  scaleAroundWorldPoint,
  screenPointToWorld,
  snapBoundsTranslation,
  transformPoint,
  unionBounds,
  boundsIntersect,
  zoomAtScreenPoint,
} from '#/lib/editor-transform'
import type { Alignment, CropHandle, CropRect } from '#/lib/editor-transform'
import type {
  Asset,
  EditorDocument,
  EditorObject,
  EditorProject,
  LayerSet,
} from '#/lib/api'

export const Route = createFileRoute('/app/editor/$projectId')({
  component: ImageEditorPage,
})

type SaveState =
  'saved' | 'dirty' | 'saving' | 'offline' | 'conflict' | 'invalid'
type LayerSettings = LayerDecompositionSettings
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
  const [documentState, setDocumentState] = useState<EditorDocument | null>(
    null,
  )
  const [documentUnsupported, setDocumentUnsupported] = useState(false)
  const [structuredProject, setStructuredProject] = useState<
    (EditorProject & { document: EditorDocumentV2 }) | null
  >(null)
  const [enteringStructured, setEnteringStructured] = useState(false)
  const [, setHistoryRevision] = useState(0)
  const [selectedID, setSelectedID] = useState('')
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(new Set())
  const [cropSession, setCropSession] = useState<{
    objectID: string
    original?: CropRect
    draft: CropRect
  }>()
  const [marquee, setMarquee] = useState<{
    left: number
    top: number
    right: number
    bottom: number
  }>()
  const [view, setView] = useState({ zoom: 100, panX: 0, panY: 0 })
  const [rendererMode, setRendererMode] = useState<'dom' | 'pixi'>('dom')
  const [pixiPresented, setPixiPresented] = useState(false)
  const [spacePressed, setSpacePressed] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const [snapGuides, setSnapGuides] = useState<
    { axis: 'x' | 'y'; position: number }[]
  >([])
  const [canvasDraft, setCanvasDraft] = useState({ width: 1, height: 1 })
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
  const [pendingLayerSet, setPendingLayerSet] = useState<LayerSet>()
  const revisionRef = useRef(0)
  const serverDocumentVersionRef = useRef<1 | 2>(1)
  const dirtyRef = useRef(false)
  const saveTimerRef = useRef<number | undefined>(undefined)
  const savePromiseRef = useRef<Promise<void> | null>(null)
  const documentRef = useRef<EditorDocument | null>(null)
  const persistedDocumentRef = useRef<EditorDocument | EditorDocumentV2 | null>(
    null,
  )
  const historyRef = useRef(new EditorHistory(100))
  const continuousHistoryRef = useRef<EditorDocument | null>(null)
  const viewportRef = useRef<HTMLDivElement>(null)
  const uploadInputRef = useRef<HTMLInputElement>(null)
  const uploadEditorImageRef = useRef<
    (file?: File, position?: { x: number; y: number }) => void
  >(() => undefined)
  const fittedRef = useRef(false)
  const zoom = view.zoom

  useEffect(() => {
    if (new URLSearchParams(window.location.search).get('renderer') === 'pixi')
      setRendererMode('pixi')
  }, [])

  useEffect(() => {
    if (!projectQuery.data || documentRef.current) return
    serverDocumentVersionRef.current = projectQuery.data.document.schema_version
    if (projectQuery.data.document.schema_version === 2) {
      setStructuredProject({
        ...projectQuery.data,
        document: structuredClone(projectQuery.data.document),
      })
      return
    }
    const requestedV2 =
      new URLSearchParams(window.location.search).get('document') === 'v2'
    const persisted = requestedV2
      ? migrateEditorDocumentV1ToV2(projectQuery.data.document)
      : projectQuery.data.document
    let projectedDocument: EditorDocument
    try {
      projectedDocument =
        persisted.schema_version === 2
          ? projectFlatEditorDocumentV2(persisted)
          : persisted
    } catch {
      setDocumentUnsupported(true)
      return
    }
    persistedDocumentRef.current = persisted
    documentRef.current = projectedDocument
    setDocumentState(projectedDocument)
    setCanvasDraft(projectedDocument.canvas)
    revisionRef.current = projectQuery.data.revision
    const initialSelection = projectedDocument.objects.at(-1)?.id ?? ''
    setSelectedID(initialSelection)
    setSelectedIDs(new Set(initialSelection ? [initialSelection] : []))
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
    if (!documentState || !selectedID) return
    const valid = new Set(documentState.objects.map((item) => item.id))
    const next = new Set([...selectedIDs].filter((id) => valid.has(id)))
    if (valid.has(selectedID) && next.has(selectedID)) return
    const fallback = [...next].at(-1) ?? documentState.objects.at(-1)?.id ?? ''
    setSelectedID(fallback)
    setSelectedIDs(new Set(fallback && !next.size ? [fallback] : next))
  }, [documentState, selectedID, selectedIDs])

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
  const saveNow = useCallback(async () => {
    if (!dirtyRef.current || !persistedDocumentRef.current) return
    if (savePromiseRef.current) return savePromiseRef.current
    const documentToSave = structuredClone(persistedDocumentRef.current)
    const signature = JSON.stringify(documentToSave)
    setSaveState('saving')
    const task = saveEditorDocument(
      `/api/v1/editor-projects/${projectId}/document`,
      revisionRef.current,
      documentToSave,
    )
      .then((result) => {
        revisionRef.current = result.revision
        serverDocumentVersionRef.current = documentToSave.schema_version
        if (JSON.stringify(persistedDocumentRef.current) === signature) {
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

  async function flushSaves() {
    window.clearTimeout(saveTimerRef.current)
    while (dirtyRef.current) {
      if (savePromiseRef.current) await savePromiseRef.current
      else await saveNow()
    }
  }

  useEffect(
    () => () => {
      window.clearTimeout(saveTimerRef.current)
    },
    [],
  )

  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (!dirtyRef.current) return
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', beforeUnload)
    return () => window.removeEventListener('beforeunload', beforeUnload)
  }, [])

  function applyDocument(next: EditorDocument, remember = true) {
    if (!documentRef.current || operationRunning) return
    if (remember && historyRef.current.commit(documentRef.current, next))
      setHistoryRevision((value) => value + 1)
    setEditorView(next)
    scheduleSave()
  }

  function setEditorView(next: EditorDocument) {
    const persisted = persistedDocumentRef.current
    persistedDocumentRef.current =
      persisted?.schema_version === 2
        ? applyFlatEditorViewToV2(persisted, next)
        : next
    documentRef.current = next
    setDocumentState(next)
  }

  function selectOnly(id = '') {
    if (cropSession && id !== cropSession.objectID) return
    setSelectedID(id)
    setSelectedIDs(new Set(id ? [id] : []))
  }

  function toggleSelection(id: string) {
    if (cropSession) return
    setSelectedIDs((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      setSelectedID(next.has(id) ? id : ([...next].at(-1) ?? ''))
      return next
    })
  }

  function updateObject(
    id: string,
    update: (object: EditorObject) => EditorObject,
    remember = true,
  ) {
    if (!documentRef.current) return
    applyDocument(
      {
        ...documentRef.current,
        objects: documentRef.current.objects.map((object) =>
          object.id === id ? update(object) : object,
        ),
      },
      remember,
    )
  }

  function beginContinuousEdit() {
    if (!continuousHistoryRef.current && documentRef.current)
      continuousHistoryRef.current = documentRef.current
  }

  function finishContinuousEdit() {
    const previous = continuousHistoryRef.current
    continuousHistoryRef.current = null
    if (
      !previous ||
      JSON.stringify(previous) === JSON.stringify(documentRef.current)
    )
      return
    if (historyRef.current.commit(previous, documentRef.current!))
      setHistoryRevision((value) => value + 1)
  }

  function moveLayerTo(sourceID: string, targetID: string) {
    const current = documentRef.current
    if (!current || sourceID === targetID || operationRunning) return
    const ordered = [...current.objects].sort((a, b) => a.z_index - b.z_index)
    const sourceIndex = ordered.findIndex((item) => item.id === sourceID)
    const targetIndex = ordered.findIndex((item) => item.id === targetID)
    if (sourceIndex < 0 || targetIndex < 0) return
    const [source] = ordered.splice(sourceIndex, 1)
    ordered.splice(targetIndex, 0, source)
    applyDocument({
      ...current,
      objects: ordered.map((item, index) => ({ ...item, z_index: index })),
    })
    selectOnly(sourceID)
  }

  function undo() {
    if (!documentRef.current || !historyRef.current.canUndo || operationRunning)
      return
    const previous = historyRef.current.undo(documentRef.current)
    setEditorView(previous)
    setHistoryRevision((value) => value + 1)
    scheduleSave()
  }

  function redo() {
    if (!documentRef.current || !historyRef.current.canRedo || operationRunning)
      return
    const next = historyRef.current.redo(documentRef.current)
    setEditorView(next)
    setHistoryRevision((value) => value + 1)
    scheduleSave()
  }

  async function leaveEditor() {
    if (cropSession) {
      setNotice('请先应用或取消当前裁切')
      return
    }
    try {
      await Promise.race([
        flushSaves(),
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

  async function enterStructuredEditor() {
    if (!documentRef.current || !persistedDocumentRef.current) return
    if (cropSession || operationRunning) {
      setNotice('请先完成当前裁切或智能分层任务')
      return
    }
    setEnteringStructured(true)
    try {
      await flushSaves()
      const current = persistedDocumentRef.current
      const next =
        current.schema_version === 2
          ? structuredClone(current)
          : migrateEditorDocumentV1ToV2(documentRef.current)
      if (serverDocumentVersionRef.current !== 2) {
        const saved = await saveEditorDocument(
          `/api/v1/editor-projects/${projectId}/document`,
          revisionRef.current,
          next,
        )
        revisionRef.current = saved.revision
        serverDocumentVersionRef.current = 2
      }
      const base = projectQuery.data
      if (!base) return
      const project = {
        ...base,
        document: next,
        revision: revisionRef.current,
      }
      persistedDocumentRef.current = next
      dirtyRef.current = false
      queryClient.setQueryData(['editor-project', projectId], project)
      setStructuredProject(project)
    } catch (error) {
      setNotice(
        error instanceof APIError ? error.message : '无法进入专业图层，请重试',
      )
    } finally {
      setEnteringStructured(false)
    }
  }

  async function returnToWorkspace() {
    const back =
      sessionStorage.getItem('cornfield:editor:return') || '/app/create'
    if (back === '/app/assets') await navigate({ to: '/app/assets' })
    else await navigate({ to: '/app/create' })
  }

  function downloadDocument() {
    if (!persistedDocumentRef.current) return
    const blob = new Blob(
      [JSON.stringify(persistedDocumentRef.current, null, 2)],
      {
        type: 'application/json',
      },
    )
    const href = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = href
    link.download = `${projectQuery.data?.name || 'cornfield-editor'}.json`
    link.click()
    URL.revokeObjectURL(href)
  }

  function beginDrag(event: ReactPointerEvent, dragIDs: Set<string>) {
    if (
      spacePressed ||
      event.button === 1 ||
      operationRunning ||
      !documentRef.current
    )
      return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    const startX = event.clientX
    const startY = event.clientY
    const initialDocument = documentRef.current
    const movableIDs = new Set(
      initialDocument.objects
        .filter((item) => dragIDs.has(item.id) && !item.locked)
        .map((item) => item.id),
    )
    if (!movableIDs.size) return
    const initialTransforms = new Map(
      initialDocument.objects
        .filter((item) => movableIDs.has(item.id))
        .map((item) => [item.id, item.transform]),
    )
    const movingBounds = unionBounds(
      initialDocument.objects.flatMap((item) => {
        if (!movableIDs.has(item.id)) return []
        const asset = assetsQuery.data?.get(item.asset_id)
        return asset
          ? [objectBounds(item.transform, asset.width, asset.height)]
          : []
      }),
    )
    let changed = false
    const snapTargets = [
      {
        left: 0,
        top: 0,
        right: initialDocument.canvas.width,
        bottom: initialDocument.canvas.height,
        centerX: initialDocument.canvas.width / 2,
        centerY: initialDocument.canvas.height / 2,
        width: initialDocument.canvas.width,
        height: initialDocument.canvas.height,
      },
      ...initialDocument.objects.flatMap((item) => {
        if (movableIDs.has(item.id) || !item.visible) return []
        const itemAsset = assetsQuery.data?.get(item.asset_id)
        return itemAsset
          ? [objectBounds(item.transform, itemAsset.width, itemAsset.height)]
          : []
      }),
    ]
    const move = (moveEvent: PointerEvent) => {
      const current = documentRef.current
      if (!current) return
      let dx = (moveEvent.clientX - startX) / (zoom / 100)
      let dy = (moveEvent.clientY - startY) / (zoom / 100)
      if (movingBounds && !moveEvent.altKey) {
        const translatedBounds = {
          ...movingBounds,
          left: movingBounds.left + dx,
          right: movingBounds.right + dx,
          centerX: movingBounds.centerX + dx,
          top: movingBounds.top + dy,
          bottom: movingBounds.bottom + dy,
          centerY: movingBounds.centerY + dy,
        }
        const snapped = snapBoundsTranslation(
          translatedBounds,
          snapTargets,
          8 / (zoom / 100),
        )
        dx += snapped.dx
        dy += snapped.dy
        setSnapGuides(snapped.guides)
      } else setSnapGuides([])
      changed = Math.abs(dx) > 1e-6 || Math.abs(dy) > 1e-6
      const next = {
        ...current,
        objects: current.objects.map((item) =>
          movableIDs.has(item.id)
            ? translateObject(item, initialTransforms.get(item.id)!, dx, dy)
            : item,
        ),
      } satisfies EditorDocument
      setEditorView(next)
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      if (!changed) {
        setSnapGuides([])
        return
      }
      historyRef.current.commit(initialDocument, documentRef.current!)
      setHistoryRevision((value) => value + 1)
      setSnapGuides([])
      scheduleSave()
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
  }

  function beginPan(event: ReactPointerEvent<HTMLDivElement>) {
    event.currentTarget.focus({ preventScroll: true })
    if (!(spacePressed || event.button === 1)) {
      if (cropSession) return
      if (event.button !== 0 || operationRunning) return
      if (
        event.target instanceof Element &&
        event.target.closest(
          '.editor-canvas > img, .editor-object-hit, .editor-selection-box',
        )
      )
        return
      event.preventDefault()
      const rect = event.currentTarget.getBoundingClientRect()
      const start = screenPointToWorld(event.clientX, event.clientY, rect, view)
      const move = (moveEvent: PointerEvent) => {
        const end = screenPointToWorld(
          moveEvent.clientX,
          moveEvent.clientY,
          rect,
          view,
        )
        setMarquee({
          left: Math.min(start.x, end.x),
          top: Math.min(start.y, end.y),
          right: Math.max(start.x, end.x),
          bottom: Math.max(start.y, end.y),
        })
      }
      const up = (upEvent: PointerEvent) => {
        window.removeEventListener('pointermove', move)
        window.removeEventListener('pointerup', up)
        const end = screenPointToWorld(
          upEvent.clientX,
          upEvent.clientY,
          rect,
          view,
        )
        const selectionBounds = boundsFromPoints(start, end)
        const dragged =
          Math.abs(upEvent.clientX - event.clientX) > 3 ||
          Math.abs(upEvent.clientY - event.clientY) > 3
        const matched = dragged
          ? (documentRef.current?.objects.filter((item) => {
              if (!item.visible) return false
              const asset = assetsQuery.data?.get(item.asset_id)
              return (
                asset &&
                boundsIntersect(
                  selectionBounds,
                  objectBounds(item.transform, asset.width, asset.height),
                )
              )
            }) ?? [])
          : []
        const next = upEvent.shiftKey ? new Set(selectedIDs) : new Set<string>()
        for (const item of matched) next.add(item.id)
        setSelectedIDs(next)
        setSelectedID(
          [...matched].sort((a, b) => b.z_index - a.z_index).at(0)?.id ??
            [...next].at(-1) ??
            '',
        )
        setMarquee(undefined)
      }
      window.addEventListener('pointermove', move)
      window.addEventListener('pointerup', up, { once: true })
      return
    }
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
    viewportRef.current?.focus({ preventScroll: true })
    event.preventDefault()
    event.stopPropagation()
    const viewport = viewportRef.current
    if (!viewport) return
    const initialDocument = documentRef.current
    const initialObject = object
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
        const degrees = ((angle - startAngle) * 180) / Math.PI
        transformed = rotateAroundCenter(
          initialObject,
          asset.width,
          asset.height,
          moveEvent.shiftKey ? Math.round(degrees / 15) * 15 : degrees,
        )
      }
      const next = {
        ...current,
        objects: current.objects.map((item) =>
          item.id === object.id ? transformed : item,
        ),
      }
      setEditorView(next)
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      historyRef.current.commit(initialDocument, documentRef.current!)
      setHistoryRevision((value) => value + 1)
      scheduleSave()
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
  }

  function startCrop() {
    if (
      !selected ||
      !selectedAsset ||
      selected.locked ||
      !selected.visible ||
      selectedIDs.size !== 1 ||
      operationRunning
    )
      return
    setSettingsOpen(false)
    setCropSession({
      objectID: selected.id,
      original: selected.crop ? { ...selected.crop } : undefined,
      draft: selected.crop
        ? { ...selected.crop }
        : { x: 0, y: 0, width: 1, height: 1 },
    })
    setNotice('拖动边缘或角点调整裁切区域')
  }

  function cancelCrop() {
    setCropSession(undefined)
    setNotice('已取消裁切')
  }

  function applyCrop() {
    if (!cropSession || !selected || selected.id !== cropSession.objectID)
      return
    const crop = roundedCrop(cropSession.draft)
    const nextCrop = isFullCrop(crop) ? undefined : crop
    if (sameCrop(cropSession.original, nextCrop)) {
      setCropSession(undefined)
      return
    }
    setCropSession(undefined)
    updateObject(selected.id, (object) => ({ ...object, crop: nextCrop }))
    setNotice(nextCrop ? '裁切已应用' : '已恢复完整图片')
  }

  function beginCropTransform(
    event: ReactPointerEvent<HTMLElement>,
    handle: CropHandle,
  ) {
    if (
      !cropSession ||
      !selected ||
      !selectedAsset ||
      selected.id !== cropSession.objectID
    )
      return
    const viewport = viewportRef.current
    const inverse = invertAffine(selected.transform)
    if (!viewport || !inverse) return
    event.preventDefault()
    event.stopPropagation()
    event.currentTarget.setPointerCapture(event.pointerId)
    const initial = { ...cropSession.draft }
    const rect = viewport.getBoundingClientRect()
    const localPoint = (clientX: number, clientY: number) => {
      const world = screenPointToWorld(clientX, clientY, rect, view)
      const local = transformPoint(inverse, world.x, world.y)
      return {
        x: local.x / selectedAsset.width,
        y: local.y / selectedAsset.height,
      }
    }
    const start = localPoint(event.clientX, event.clientY)
    const move = (moveEvent: PointerEvent) => {
      const point = localPoint(moveEvent.clientX, moveEvent.clientY)
      const dx = point.x - start.x
      const dy = point.y - start.y
      const draft =
        handle === 'move'
          ? moveCrop(initial, dx, dy)
          : resizeCrop(
              initial,
              handle,
              dx,
              dy,
              Math.min(1, 32 / selectedAsset.width),
              Math.min(1, 32 / selectedAsset.height),
            )
      setCropSession((current) =>
        current?.objectID === selected.id ? { ...current, draft } : current,
      )
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up, { once: true })
  }

  function beginGroupTransform(
    event: ReactPointerEvent<HTMLButtonElement>,
    kind: 'scale' | 'rotate',
  ) {
    const current = documentRef.current
    const viewport = viewportRef.current
    if (!current || !viewport || operationRunning || !groupBounds) return
    const transformable = current.objects.filter(
      (item) => selectedIDs.has(item.id) && item.visible && !item.locked,
    )
    if (transformable.length !== selectedIDs.size) {
      setNotice('请先解锁并显示所选图层')
      return
    }
    viewport.focus({ preventScroll: true })
    event.preventDefault()
    event.stopPropagation()
    const initialDocument = current
    const initialTransforms = new Map(
      transformable.map((item) => [item.id, item.transform]),
    )
    const center = { x: groupBounds.centerX, y: groupBounds.centerY }
    const rect = viewport.getBoundingClientRect()
    const scale = view.zoom / 100
    const screenCenter = {
      x: rect.left + rect.width / 2 + view.panX + center.x * scale,
      y: rect.top + rect.height / 2 + view.panY + center.y * scale,
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
    const objectScales = transformable.map((item) =>
      objectScale(item.transform),
    )
    const minFactor = Math.max(...objectScales.map((value) => 0.05 / value))
    const maxFactor = Math.min(...objectScales.map((value) => 8 / value))
    let changed = false
    const move = (moveEvent: PointerEvent) => {
      const latest = documentRef.current
      if (!latest) return
      let factor = 1
      let degrees = 0
      if (kind === 'scale') {
        const distance = Math.hypot(
          moveEvent.clientX - screenCenter.x,
          moveEvent.clientY - screenCenter.y,
        )
        factor = Math.min(
          maxFactor,
          Math.max(minFactor, distance / startDistance),
        )
        changed = Math.abs(factor - 1) > 1e-6
      } else {
        const angle = Math.atan2(
          moveEvent.clientY - screenCenter.y,
          moveEvent.clientX - screenCenter.x,
        )
        degrees = ((angle - startAngle) * 180) / Math.PI
        if (moveEvent.shiftKey) degrees = Math.round(degrees / 15) * 15
        changed = Math.abs(degrees) > 1e-6
      }
      const next = {
        ...latest,
        objects: latest.objects.map((item) => {
          const initial = initialTransforms.get(item.id)
          if (!initial) return item
          return {
            ...item,
            transform:
              kind === 'scale'
                ? scaleAroundWorldPoint(initial, center, factor)
                : rotateAroundWorldPoint(initial, center, degrees),
          }
        }),
      } satisfies EditorDocument
      setEditorView(next)
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      if (!changed) return
      historyRef.current.commit(initialDocument, documentRef.current!)
      setHistoryRevision((value) => value + 1)
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

  async function uploadEditorImage(
    file?: File,
    position?: { x: number; y: number },
  ) {
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
      const center = position ?? {
        x: current.canvas.width / 2,
        y: current.canvas.height / 2,
      }
      const nextObject: EditorObject = {
        id: crypto.randomUUID(),
        name: file.name.replace(/\.[^.]+$/, '').slice(0, 64) || '导入图片',
        asset_id: asset.id,
        transform: [
          scale,
          0,
          0,
          scale,
          center.x - (asset.width * scale) / 2,
          center.y - (asset.height * scale) / 2,
        ],
        opacity: 1,
        visible: true,
        locked: false,
        z_index: current.objects.length,
      }
      applyDocument({ ...current, objects: [...current.objects, nextObject] })
      selectOnly(nextObject.id)
      setNotice('图片已置入画板')
    } catch (error) {
      setNotice(error instanceof Error ? error.message : '图片上传失败')
    } finally {
      setUploading(false)
      if (uploadInputRef.current) uploadInputRef.current.value = ''
    }
  }

  async function importFiles(
    files: FileList | File[],
    position?: { x: number; y: number },
  ) {
    const accepted = Array.from(files).filter((item) =>
      acceptedUploadTypes.has(item.type),
    )
    if (!accepted.length) {
      setNotice('拖入 JPEG、PNG 或 WebP 图片即可添加图层')
      return
    }
    const remaining = Math.max(
      0,
      maxEditorObjects - (documentRef.current?.objects.length ?? 0),
    )
    if (!remaining) {
      setNotice('当前工程最多可放置 64 个图层')
      return
    }
    for (const [index, file] of accepted.slice(0, remaining).entries()) {
      await uploadEditorImage(
        file,
        position
          ? { x: position.x + index * 24, y: position.y + index * 24 }
          : undefined,
      )
    }
  }

  function duplicateSelectedObjects() {
    const current = documentRef.current
    if (!current || operationRunning) return
    const sources = current.objects.filter((item) => selectedIDs.has(item.id))
    if (!sources.length) return
    if (current.objects.length + sources.length > maxEditorObjects) {
      setNotice('当前工程最多可放置 64 个图层')
      return
    }
    const duplicates = sources
      .sort((a, b) => a.z_index - b.z_index)
      .map((source, index): EditorObject => ({
        ...structuredClone(source),
        id: crypto.randomUUID(),
        name: `${source.name || '图层'} 副本`.slice(0, 64),
        z_index: current.objects.length + index,
        transform: [
          source.transform[0],
          source.transform[1],
          source.transform[2],
          source.transform[3],
          source.transform[4] + 24,
          source.transform[5] + 24,
        ],
      }))
    applyDocument({ ...current, objects: [...current.objects, ...duplicates] })
    setSelectedIDs(new Set(duplicates.map((item) => item.id)))
    setSelectedID(duplicates.at(-1)?.id ?? '')
    setNotice(`已复制 ${duplicates.length} 个图层`)
  }

  uploadEditorImageRef.current = (file, position) =>
    void uploadEditorImage(file, position)

  useEffect(() => {
    const paste = (event: ClipboardEvent) => {
      if (isTypingTarget(event.target)) return
      const file = Array.from(event.clipboardData?.files ?? []).find((item) =>
        acceptedUploadTypes.has(item.type),
      )
      if (!file) return
      event.preventDefault()
      uploadEditorImageRef.current(file)
    }
    window.addEventListener('paste', paste)
    return () => window.removeEventListener('paste', paste)
  }, [])

  function applyLayerSet(layerSet: LayerSet) {
    if (!documentRef.current) return
    const objects: EditorObject[] = [
      {
        id: `base-${layerSet.id}`,
        name: '背景',
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
          name: item.name || `图层 ${item.z_index}`,
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
    applyDocument({ ...documentRef.current, objects })
    selectOnly(objects.at(-1)?.id ?? '')
    setPendingLayerSet(undefined)
  }

  const operations = useEditorOperations({
    projectID: projectId,
    initialOperationID: projectQuery.data?.latest_operation_id,
    activeLayerSet: projectQuery.data?.active_layer_set,
    getRevision: () => revisionRef.current,
    flushSaves,
    onLayerSetReady: (layerSet, sourceRevision) => {
      if (sourceRevision === revisionRef.current && layerSet.applied_to_project)
        applyLayerSet(layerSet)
      else if (!layerSet.applied_to_project) setPendingLayerSet(layerSet)
    },
    onNotice: setNotice,
  })
  const operation = operations.operation
  const currentLayerSet = operations.currentLayerSet
  const operationRunning = operations.running
  const canDecompose = operations.canDecompose
  const elapsed = operations.elapsed

  async function startDecomposition(confirmed = false) {
    if (currentLayerSet && !confirmed) {
      setRerunConfirm(true)
      return
    }
    if (await operations.startDecomposition(settings)) setSettingsOpen(false)
  }

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
  const selectedBounds =
    selected && selectedAsset
      ? objectBounds(
          selected.transform,
          selectedAsset.width,
          selectedAsset.height,
        )
      : undefined
  const selectedAxisScales = selected
    ? objectAxisScales(selected.transform)
    : undefined
  const selectedObjects =
    documentState?.objects.filter((object) => selectedIDs.has(object.id)) ?? []
  const groupBounds = unionBounds(
    selectedObjects.flatMap((object) => {
      if (!object.visible) return []
      const asset = objectAssets?.get(object.asset_id)
      return asset
        ? [objectBounds(object.transform, asset.width, asset.height)]
        : []
    }),
  )

  function removeSelectedObjects() {
    const current = documentRef.current
    if (!selected || !current || operationRunning) return
    const removable = new Set(
      selectedObjects
        .filter(
          (item) =>
            !item.locked &&
            item.asset_id !== projectQuery.data?.source_asset_id,
        )
        .map((item) => item.id),
    )
    if (!removable.size) {
      setNotice(
        selectedObjects.some((item) => item.locked)
          ? '请先解锁需要删除的图层'
          : '源图是当前工程的锚点，可以隐藏，但不能移除',
      )
      return
    }
    if (current.objects.length - removable.size < 1) {
      setNotice('画板至少需要保留一个图层')
      return
    }
    const remaining = current.objects
      .filter((object) => !removable.has(object.id))
      .map((object, index) => ({ ...object, z_index: index }))
    applyDocument({ ...current, objects: remaining })
    selectOnly(remaining.at(-1)?.id ?? '')
  }

  function updateSelectedObjects(
    update: (object: EditorObject) => EditorObject,
  ) {
    const current = documentRef.current
    if (!current || !selectedIDs.size || operationRunning) return
    applyDocument({
      ...current,
      objects: current.objects.map((item) =>
        selectedIDs.has(item.id) ? update(item) : item,
      ),
    })
  }

  function selectedObjectBounds() {
    return selectedObjects.flatMap((object) => {
      if (!object.visible) return []
      const asset = objectAssets?.get(object.asset_id)
      return asset
        ? [
            {
              object,
              bounds: objectBounds(object.transform, asset.width, asset.height),
            },
          ]
        : []
    })
  }

  function alignSelectedObjects(alignment: Alignment) {
    const current = documentRef.current
    const items = selectedObjectBounds()
    if (!current || items.length < 2 || operationRunning) return
    const movable = items.filter((item) => !item.object.locked)
    const locked = items.filter((item) => item.object.locked)
    const target = unionBounds(
      (locked.length ? locked : items).map((item) => item.bounds),
    )
    if (!target || !movable.length) {
      setNotice('所选图层均已锁定')
      return
    }
    const updates = new Map(
      movable.map(({ object, bounds }) => [
        object.id,
        alignmentOffset(bounds, target, alignment),
      ]),
    )
    if (
      ![...updates.values()].some(
        ({ dx, dy }) => Math.abs(dx) > 1e-6 || Math.abs(dy) > 1e-6,
      )
    )
      return
    applyDocument({
      ...current,
      objects: current.objects.map((item) => {
        const offset = updates.get(item.id)
        return offset
          ? translateObject(item, item.transform, offset.dx, offset.dy)
          : item
      }),
    })
  }

  function distributeSelectedObjects(axis: 'x' | 'y') {
    const current = documentRef.current
    const items = selectedObjectBounds()
    if (!current || operationRunning) return
    if (items.length < 3) {
      setNotice('至少选择 3 个可见图层才能等距分布')
      return
    }
    if (items.some((item) => item.object.locked)) {
      setNotice('请先解锁参与分布的图层')
      return
    }
    const offsets = distributionOffsets(
      items.map(({ object, bounds }) => ({ id: object.id, bounds })),
      axis,
    )
    if (![...offsets.values()].some((offset) => Math.abs(offset) > 1e-6)) return
    applyDocument({
      ...current,
      objects: current.objects.map((item) => {
        const offset = offsets.get(item.id)
        if (offset === undefined) return item
        return translateObject(
          item,
          item.transform,
          axis === 'x' ? offset : 0,
          axis === 'y' ? offset : 0,
        )
      }),
    })
  }

  function resizeArtboard() {
    const current = documentRef.current
    const width = Math.round(canvasDraft.width)
    const height = Math.round(canvasDraft.height)
    if (
      !current ||
      width < 1 ||
      height < 1 ||
      width > 8192 ||
      height > 8192 ||
      width * height > 36_000_000
    ) {
      setNotice('画板需在 8192px、3600万像素以内')
      return
    }
    if (width === current.canvas.width && height === current.canvas.height)
      return
    applyDocument({ ...current, canvas: { width, height } })
    setCanvasDraft({ width, height })
    requestAnimationFrame(fitCanvas)
  }

  function updateSelectedCenter(
    axis: 'x' | 'y',
    value: number,
    remember = true,
  ) {
    if (
      !selected ||
      !selectedAsset ||
      !selectedBounds ||
      !Number.isFinite(value)
    )
      return
    updateObject(
      selected.id,
      (object) =>
        moveObjectCenter(
          object,
          selectedAsset.width,
          selectedAsset.height,
          axis === 'x' ? value : selectedBounds.centerX,
          axis === 'y' ? value : selectedBounds.centerY,
        ),
      remember,
    )
  }

  function updateSelectedSize(
    axis: 'width' | 'height',
    value: number,
    remember = true,
  ) {
    if (!selected || !selectedAsset || !Number.isFinite(value) || value <= 0)
      return
    const desiredScale = Math.min(
      8,
      Math.max(
        0.05,
        axis === 'width'
          ? value / selectedAsset.width
          : value / selectedAsset.height,
      ),
    )
    const currentScale =
      axis === 'width'
        ? objectAxisScales(selected.transform).x
        : objectAxisScales(selected.transform).y
    updateObject(
      selected.id,
      (object) =>
        scaleByFactorAroundCenter(
          object,
          selectedAsset.width,
          selectedAsset.height,
          desiredScale / currentScale,
        ),
      remember,
    )
  }
  const sortedObjects = useMemo(
    () =>
      [...(documentState?.objects ?? [])].sort((a, b) => b.z_index - a.z_index),
    [documentState],
  )

  if (projectQuery.isError)
    return (
      <AppShell immersive>
        <main className="editor-loading">图片编辑工程无法打开</main>
      </AppShell>
    )
  if (structuredProject)
    return (
      <StructuredEditor
        project={structuredProject}
        onBack={() => void returnToWorkspace()}
        onProjectChange={(project) => {
          if (project.document.schema_version !== 2) return
          const next = { ...project, document: project.document }
          setStructuredProject(next)
          queryClient.setQueryData(['editor-project', projectId], next)
        }}
      />
    )
  if (documentUnsupported)
    return (
      <AppShell immersive>
        <main className="editor-loading">
          当前工程包含图层组或蒙版，请使用新版图层面板继续编辑。
          <button type="button" onClick={() => void returnToWorkspace()}>
            返回工作区
          </button>
        </main>
      </AppShell>
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
              disabled={
                !historyRef.current.canUndo ||
                operationRunning ||
                Boolean(cropSession)
              }
              onClick={undo}
            >
              <Undo2 size={16} />
            </button>
            <button
              type="button"
              aria-label="重做"
              disabled={
                !historyRef.current.canRedo ||
                operationRunning ||
                Boolean(cropSession)
              }
              onClick={redo}
            >
              <Redo2 size={16} />
            </button>
          </div>
          <div className="editor-topbar-group editor-primary-actions">
            <button
              type="button"
              className="editor-structured-entry"
              disabled={
                enteringStructured || operationRunning || Boolean(cropSession)
              }
              onClick={() => void enterStructuredEditor()}
            >
              <Layers3 size={16} />
              {enteringStructured ? '正在切换' : '专业图层'}
            </button>
            <button
              type="button"
              className="editor-layer-settings"
              aria-label="智能分层设置"
              disabled={Boolean(cropSession)}
              onClick={() => setSettingsOpen((value) => !value)}
            >
              <Layers3 size={16} />
            </button>
            <button
              type="button"
              className="editor-decompose"
              disabled={
                operationRunning || !canDecompose || Boolean(cropSession)
              }
              title={
                canDecompose
                  ? '智能分层'
                  : operations.capabilityMessage || '智能分层暂未开放'
              }
              onClick={() => void startDecomposition()}
            >
              <Sparkles size={16} />
              智能分层
            </button>
            <button
              type="button"
              className="editor-publish"
              disabled={operationRunning || Boolean(cropSession)}
              onClick={() => void operations.publish()}
            >
              <Save size={16} />
              保存为新图片
            </button>
          </div>
        </header>

        <section
          className="editor-body"
          data-cropping={cropSession ? true : undefined}
        >
          <aside className="editor-tools" aria-label="基础编辑工具">
            <input
              ref={uploadInputRef}
              className="sr-only"
              type="file"
              multiple
              accept="image/jpeg,image/png,image/webp"
              onChange={(event) =>
                void importFiles(event.currentTarget.files ?? [])
              }
            />
            <button
              type="button"
              title="添加图片"
              aria-label="添加图片"
              disabled={uploading || operationRunning || Boolean(cropSession)}
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
              disabled={
                !selected ||
                selectedIDs.size !== 1 ||
                operationRunning ||
                Boolean(cropSession)
              }
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
              disabled={
                !selected ||
                selectedIDs.size !== 1 ||
                operationRunning ||
                Boolean(cropSession)
              }
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
              disabled={
                !selected ||
                selectedIDs.size !== 1 ||
                operationRunning ||
                Boolean(cropSession)
              }
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
              title="裁切图层"
              aria-label="裁切图层"
              data-testid="editor-crop-tool"
              className={cropSession ? 'active' : ''}
              disabled={
                !selected ||
                selected.locked ||
                !selected.visible ||
                selectedIDs.size !== 1 ||
                operationRunning ||
                Boolean(cropSession)
              }
              onClick={startCrop}
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
            data-drag-over={dragOver || undefined}
            onPointerDown={beginPan}
            onDragEnter={(event) => {
              if (!event.dataTransfer.types.includes('Files')) return
              event.preventDefault()
              setDragOver(true)
            }}
            onDragOver={(event) => {
              if (!event.dataTransfer.types.includes('Files')) return
              event.preventDefault()
              event.dataTransfer.dropEffect = 'copy'
              setDragOver(true)
            }}
            onDragLeave={(event) => {
              if (event.currentTarget.contains(event.relatedTarget as Node))
                return
              setDragOver(false)
            }}
            onDrop={(event) => {
              event.preventDefault()
              setDragOver(false)
              const rect = event.currentTarget.getBoundingClientRect()
              const position = screenPointToWorld(
                event.clientX,
                event.clientY,
                rect,
                view,
              )
              void importFiles(event.dataTransfer.files, position)
            }}
            onWheel={(event) => {
              event.preventDefault()
              if (!(event.ctrlKey || event.metaKey)) {
                setView((current) => ({
                  ...current,
                  panX:
                    current.panX -
                    (event.shiftKey ? event.deltaY : event.deltaX),
                  panY: current.panY - (event.shiftKey ? 0 : event.deltaY),
                }))
                return
              }
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
              if (cropSession) {
                if (event.key === '0') {
                  event.preventDefault()
                  fitCanvas()
                  return
                }
                if (event.key === '1') {
                  event.preventDefault()
                  changeZoom(100)
                  return
                }
                if (event.key === 'Escape') {
                  event.preventDefault()
                  cancelCrop()
                  return
                }
                if (event.key === 'Enter') {
                  event.preventDefault()
                  applyCrop()
                  return
                }
                if (
                  selectedAsset &&
                  ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(
                    event.key,
                  )
                ) {
                  event.preventDefault()
                  const distance = event.shiftKey ? 10 : 1
                  const dx =
                    event.key === 'ArrowLeft'
                      ? -distance / selectedAsset.width
                      : event.key === 'ArrowRight'
                        ? distance / selectedAsset.width
                        : 0
                  const dy =
                    event.key === 'ArrowUp'
                      ? -distance / selectedAsset.height
                      : event.key === 'ArrowDown'
                        ? distance / selectedAsset.height
                        : 0
                  setCropSession((current) =>
                    current
                      ? { ...current, draft: moveCrop(current.draft, dx, dy) }
                      : current,
                  )
                }
                return
              }
              if (
                (event.ctrlKey || event.metaKey) &&
                event.key.toLowerCase() === 'a'
              ) {
                event.preventDefault()
                const selectable = documentState.objects
                  .filter((item) => item.visible)
                  .map((item) => item.id)
                setSelectedIDs(new Set(selectable))
                setSelectedID(selectable.at(-1) ?? '')
                return
              }
              if (
                (event.ctrlKey || event.metaKey) &&
                event.key.toLowerCase() === 'z'
              ) {
                event.preventDefault()
                if (event.shiftKey) redo()
                else undo()
                return
              }
              if (
                selected &&
                (event.ctrlKey || event.metaKey) &&
                event.key.toLowerCase() === 'd'
              ) {
                event.preventDefault()
                duplicateSelectedObjects()
                return
              }
              if (event.key === 'Escape') {
                selectOnly()
                return
              }
              if (event.key === '0') {
                event.preventDefault()
                fitCanvas()
                return
              }
              if (event.key === '1') {
                event.preventDefault()
                changeZoom(100)
                return
              }
              if (
                selected &&
                selectedObjects.some((item) => !item.locked) &&
                !operationRunning &&
                (event.key === 'Delete' || event.key === 'Backspace')
              ) {
                event.preventDefault()
                removeSelectedObjects()
                return
              }
              if (
                !selected ||
                !selectedObjects.some((item) => !item.locked) ||
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
              updateSelectedObjects((object) =>
                object.locked
                  ? object
                  : {
                      ...object,
                      transform: [
                        object.transform[0],
                        object.transform[1],
                        object.transform[2],
                        object.transform[3],
                        object.transform[4] + dx,
                        object.transform[5] + dy,
                      ],
                    },
              )
            }}
          >
            {rendererMode === 'pixi' && !cropSession && pixiPresented && (
              <div
                className="editor-pixi-artboard-underlay editor-artboard"
                data-testid="editor-pixi-artboard-underlay"
                style={
                  {
                    width: documentState.canvas.width,
                    height: documentState.canvas.height,
                    transform: `translate(${view.panX}px, ${view.panY}px) scale(${zoom / 100})`,
                    '--editor-zoom': zoom / 100,
                  } as CSSProperties
                }
                aria-hidden="true"
              />
            )}
            <PixiSurface
              enabled={rendererMode === 'pixi' && !cropSession}
              document={documentState}
              assets={objectAssets}
              viewport={view}
              onUnavailable={() => {
                setRendererMode('dom')
                setPixiPresented(false)
                setNotice('图形渲染暂不可用，已安全切回兼容模式')
              }}
              onPresentedChange={setPixiPresented}
            />
            <div
              className="editor-world"
              data-renderer={
                rendererMode === 'pixi' && !cropSession && pixiPresented
                  ? 'pixi'
                  : 'dom'
              }
              style={{
                transform: `translate(${view.panX}px, ${view.panY}px) scale(${zoom / 100})`,
              }}
            >
              <span className="editor-artboard-label">
                画板 · {documentState.canvas.width} ×{' '}
                {documentState.canvas.height}
              </span>
              <div
                className={`editor-canvas editor-artboard${rendererMode === 'pixi' && !cropSession && pixiPresented ? ' is-pixi-hit-layer' : ''}`}
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
                  const visibleCrop =
                    cropSession?.objectID === object.id
                      ? undefined
                      : object.crop
                  const pixiHitLayer =
                    rendererMode === 'pixi' && !cropSession && pixiPresented
                  const objectStyle = {
                    opacity: pixiHitLayer ? 0 : object.opacity,
                    zIndex: object.z_index,
                    transform: `matrix(${object.transform.join(',')})`,
                    clipPath: visibleCrop
                      ? `inset(${visibleCrop.y * 100}% ${(1 - visibleCrop.x - visibleCrop.width) * 100}% ${(1 - visibleCrop.y - visibleCrop.height) * 100}% ${visibleCrop.x * 100}%)`
                      : undefined,
                  }
                  const pointerDown = (event: ReactPointerEvent) => {
                    if (cropSession) return
                    viewportRef.current?.focus({ preventScroll: true })
                    const nextSelection =
                      event.shiftKey || selectedIDs.has(object.id)
                        ? new Set(selectedIDs)
                        : new Set([object.id])
                    if (event.shiftKey) {
                      if (nextSelection.has(object.id))
                        nextSelection.delete(object.id)
                      else nextSelection.add(object.id)
                    }
                    if (!nextSelection.has(object.id)) {
                      setSelectedIDs(nextSelection)
                      setSelectedID([...nextSelection].at(-1) ?? '')
                      return
                    }
                    setSelectedIDs(nextSelection)
                    setSelectedID(object.id)
                    if (spacePressed || event.button === 1) return
                    beginDrag(event, nextSelection)
                  }
                  if (pixiHitLayer)
                    return (
                      <div
                        key={object.id}
                        className="editor-object-hit"
                        data-object-id={object.id}
                        style={{
                          ...objectStyle,
                          width: asset.width,
                          height: asset.height,
                        }}
                        onPointerDown={pointerDown}
                      />
                    )
                  return (
                    <img
                      key={object.id}
                      className={
                        !cropSession &&
                        selectedIDs.size === 1 &&
                        selectedIDs.has(object.id)
                          ? 'is-selected'
                          : ''
                      }
                      src={asset.thumb_1280_url || asset.thumb_640_url}
                      width={asset.width}
                      height={asset.height}
                      draggable={false}
                      alt="画布图层"
                      style={objectStyle}
                      onPointerDown={pointerDown}
                    />
                  )
                })}
              </div>
              {selected &&
                selectedAsset &&
                selected.visible &&
                !cropSession &&
                selectedIDs.size === 1 && (
                  <div
                    className="editor-selection-box"
                    style={
                      {
                        width: selectedAsset.width,
                        height: selectedAsset.height,
                        transform: `matrix(${selected.transform.join(',')})`,
                        '--editor-zoom': zoom / 100,
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
              {cropSession &&
                selected &&
                selectedAsset &&
                selected.id === cropSession.objectID && (
                  <div
                    className="editor-crop-overlay"
                    style={
                      {
                        width: selectedAsset.width,
                        height: selectedAsset.height,
                        transform: `matrix(${selected.transform.join(',')})`,
                        '--editor-handle-size': `${12 / (zoom / 100) / Math.max(0.05, objectScale(selected.transform))}px`,
                      } as CSSProperties
                    }
                  >
                    <div
                      className="editor-crop-frame"
                      style={{
                        left: cropSession.draft.x * selectedAsset.width,
                        top: cropSession.draft.y * selectedAsset.height,
                        width: cropSession.draft.width * selectedAsset.width,
                        height: cropSession.draft.height * selectedAsset.height,
                      }}
                    >
                      <button
                        className="editor-crop-move"
                        type="button"
                        aria-label="移动裁切区域"
                        onPointerDown={(event) =>
                          beginCropTransform(event, 'move')
                        }
                      />
                      <span className="editor-crop-grid is-x-one" />
                      <span className="editor-crop-grid is-x-two" />
                      <span className="editor-crop-grid is-y-one" />
                      <span className="editor-crop-grid is-y-two" />
                      {['n', 'ne', 'e', 'se', 's', 'sw', 'w', 'nw'].map(
                        (handle) => (
                          <button
                            key={handle}
                            className={`editor-crop-handle is-${handle}`}
                            type="button"
                            aria-label={`调整裁切区域 ${handle}`}
                            onPointerDown={(event) =>
                              beginCropTransform(event, handle as CropHandle)
                            }
                          />
                        ),
                      )}
                    </div>
                  </div>
                )}
              {selectedIDs.size > 1 && groupBounds && (
                <div
                  className="editor-group-selection"
                  style={
                    {
                      left: groupBounds.left,
                      top: groupBounds.top,
                      width: groupBounds.width,
                      height: groupBounds.height,
                      '--editor-zoom': zoom / 100,
                      '--editor-handle-size': `${12 / (zoom / 100)}px`,
                      '--editor-handle-distance': `${34 / (zoom / 100)}px`,
                    } as CSSProperties
                  }
                >
                  <span>{selectedIDs.size} 个图层</span>
                  {selectedObjects.every(
                    (item) => item.visible && !item.locked,
                  ) &&
                    !operationRunning && (
                      <>
                        {['nw', 'ne', 'se', 'sw'].map((position) => (
                          <button
                            key={position}
                            className={`editor-transform-handle is-${position}`}
                            type="button"
                            aria-label="缩放所选图层"
                            onPointerDown={(event) =>
                              beginGroupTransform(event, 'scale')
                            }
                          />
                        ))}
                        <button
                          className="editor-rotate-handle is-group"
                          type="button"
                          aria-label="旋转所选图层"
                          onPointerDown={(event) =>
                            beginGroupTransform(event, 'rotate')
                          }
                        />
                      </>
                    )}
                </div>
              )}
              {marquee && (
                <div
                  className="editor-marquee"
                  style={
                    {
                      left: marquee.left,
                      top: marquee.top,
                      width: marquee.right - marquee.left,
                      height: marquee.bottom - marquee.top,
                      '--editor-zoom': zoom / 100,
                    } as CSSProperties
                  }
                />
              )}
              {snapGuides.map((guide) => (
                <span
                  key={`${guide.axis}-${guide.position}`}
                  className={`editor-snap-guide is-${guide.axis}`}
                  style={
                    guide.axis === 'x'
                      ? { left: guide.position }
                      : { top: guide.position }
                  }
                />
              ))}
            </div>
            {cropSession && (
              <div
                className="editor-crop-actions"
                role="toolbar"
                aria-label="裁切操作"
              >
                <span>裁切图层</span>
                <button type="button" onClick={cancelCrop}>
                  <X size={14} /> 取消
                </button>
                <button className="primary" type="button" onClick={applyCrop}>
                  <Check size={14} /> 应用裁切
                </button>
              </div>
            )}
            {dragOver && (
              <div className="editor-drop-overlay" aria-hidden="true">
                <Plus size={22} />
                松开以添加为新图层
              </div>
            )}
            {operationRunning && (
              <EditorOperationWaiting
                status={operation?.status}
                message={operation?.message}
                elapsed={elapsed}
              />
            )}
            {pendingLayerSet && (
              <button
                className="editor-pending-result"
                type="button"
                onClick={() => applyLayerSet(pendingLayerSet)}
              >
                工程已在其他标签页更新 · 应用这次分层结果
              </button>
            )}
          </div>

          <aside className="editor-inspector">
            <div className="editor-panel-tabs">
              <strong>图层</strong>
              <span>
                {selectedIDs.size > 1
                  ? `已选 ${selectedIDs.size}`
                  : `${documentState.objects.length} / 64`}
              </span>
            </div>
            {selectedIDs.size > 1 && (
              <div className="editor-bulk-panel">
                <div className="editor-bulk-align" aria-label="多选图层布局">
                  <span>对齐</span>
                  <div>
                    <button
                      type="button"
                      title="左对齐"
                      aria-label="左对齐所选图层"
                      onClick={() => alignSelectedObjects('left')}
                    >
                      <AlignStartVertical size={14} />
                    </button>
                    <button
                      type="button"
                      title="水平居中"
                      aria-label="水平居中对齐所选图层"
                      onClick={() => alignSelectedObjects('horizontal-center')}
                    >
                      <AlignCenterVertical size={14} />
                    </button>
                    <button
                      type="button"
                      title="右对齐"
                      aria-label="右对齐所选图层"
                      onClick={() => alignSelectedObjects('right')}
                    >
                      <AlignEndVertical size={14} />
                    </button>
                    <button
                      type="button"
                      title="顶部对齐"
                      aria-label="顶部对齐所选图层"
                      onClick={() => alignSelectedObjects('top')}
                    >
                      <AlignStartHorizontal size={14} />
                    </button>
                    <button
                      type="button"
                      title="垂直居中"
                      aria-label="垂直居中对齐所选图层"
                      onClick={() => alignSelectedObjects('vertical-center')}
                    >
                      <AlignCenterHorizontal size={14} />
                    </button>
                    <button
                      type="button"
                      title="底部对齐"
                      aria-label="底部对齐所选图层"
                      onClick={() => alignSelectedObjects('bottom')}
                    >
                      <AlignEndHorizontal size={14} />
                    </button>
                    <button
                      type="button"
                      title="水平等距分布"
                      aria-label="水平等距分布所选图层"
                      disabled={selectedObjects.length < 3}
                      onClick={() => distributeSelectedObjects('x')}
                    >
                      <AlignHorizontalDistributeCenter size={14} />
                    </button>
                    <button
                      type="button"
                      title="垂直等距分布"
                      aria-label="垂直等距分布所选图层"
                      disabled={selectedObjects.length < 3}
                      onClick={() => distributeSelectedObjects('y')}
                    >
                      <AlignVerticalDistributeCenter size={14} />
                    </button>
                  </div>
                </div>
                <div
                  className="editor-bulk-layer-actions"
                  aria-label="批量图层操作"
                >
                  <button
                    type="button"
                    onClick={() =>
                      updateSelectedObjects((item) => ({
                        ...item,
                        visible: false,
                      }))
                    }
                  >
                    <EyeOff size={14} /> 隐藏
                  </button>
                  <button
                    type="button"
                    onClick={() =>
                      updateSelectedObjects((item) => ({
                        ...item,
                        visible: true,
                      }))
                    }
                  >
                    <Eye size={14} /> 显示
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      const lock = !selectedObjects.every((item) => item.locked)
                      updateSelectedObjects((item) => ({
                        ...item,
                        locked: lock,
                      }))
                    }}
                  >
                    {selectedObjects.every((item) => item.locked) ? (
                      <Unlock size={14} />
                    ) : (
                      <Lock size={14} />
                    )}
                    {selectedObjects.every((item) => item.locked)
                      ? '解锁'
                      : '锁定'}
                  </button>
                  <button type="button" onClick={duplicateSelectedObjects}>
                    <Copy size={14} /> 复制
                  </button>
                  <button type="button" onClick={removeSelectedObjects}>
                    <Trash2 size={14} /> 删除
                  </button>
                </div>
              </div>
            )}
            <div className="editor-artboard-settings">
              <span>画板尺寸</span>
              <label>
                <small>宽</small>
                <input
                  aria-label="画板宽度"
                  type="number"
                  min="1"
                  max="8192"
                  value={canvasDraft.width}
                  disabled={operationRunning}
                  onChange={(event) =>
                    setCanvasDraft((current) => ({
                      ...current,
                      width: Number(event.target.value),
                    }))
                  }
                />
              </label>
              <i>×</i>
              <label>
                <small>高</small>
                <input
                  aria-label="画板高度"
                  type="number"
                  min="1"
                  max="8192"
                  value={canvasDraft.height}
                  disabled={operationRunning}
                  onChange={(event) =>
                    setCanvasDraft((current) => ({
                      ...current,
                      height: Number(event.target.value),
                    }))
                  }
                />
              </label>
              <button
                type="button"
                disabled={operationRunning}
                onClick={resizeArtboard}
              >
                应用
              </button>
            </div>
            <div className="editor-layer-list">
              {sortedObjects.map((object) => {
                const layerName =
                  object.name ??
                  currentLayerSet?.items.find((item) => item.id === object.id)
                    ?.name ??
                  (object.z_index === 0 ? '背景' : `图层 ${object.z_index}`)
                return (
                  <div
                    key={object.id}
                    className={`editor-layer-row${selectedIDs.has(object.id) ? ' active' : ''}`}
                    draggable={!operationRunning}
                    onDragStart={(event) => {
                      event.dataTransfer.effectAllowed = 'move'
                      event.dataTransfer.setData(
                        'application/x-cornfield-layer',
                        object.id,
                      )
                    }}
                    onDragOver={(event) => {
                      if (
                        event.dataTransfer.types.includes(
                          'application/x-cornfield-layer',
                        )
                      )
                        event.preventDefault()
                    }}
                    onDrop={(event) => {
                      event.preventDefault()
                      moveLayerTo(
                        event.dataTransfer.getData(
                          'application/x-cornfield-layer',
                        ),
                        object.id,
                      )
                    }}
                  >
                    <button
                      className="editor-layer-select"
                      type="button"
                      aria-label={`选择图层 ${layerName}`}
                      aria-pressed={selectedIDs.has(object.id)}
                      onClick={(event) =>
                        event.shiftKey
                          ? toggleSelection(object.id)
                          : selectOnly(object.id)
                      }
                    >
                      <img
                        src={objectAssets?.get(object.asset_id)?.thumb_320_url}
                        alt=""
                      />
                      <span>{layerName}</span>
                    </button>
                    <div className="editor-layer-quick-actions">
                      <button
                        type="button"
                        aria-label={`${object.visible ? '隐藏' : '显示'}图层 ${layerName}`}
                        disabled={operationRunning}
                        onClick={() =>
                          updateObject(object.id, (item) => ({
                            ...item,
                            visible: !item.visible,
                          }))
                        }
                      >
                        {object.visible ? (
                          <Eye size={13} />
                        ) : (
                          <EyeOff size={13} />
                        )}
                      </button>
                      <button
                        type="button"
                        aria-label={`${object.locked ? '解锁' : '锁定'}图层 ${layerName}`}
                        disabled={operationRunning}
                        onClick={() =>
                          updateObject(object.id, (item) => ({
                            ...item,
                            locked: !item.locked,
                          }))
                        }
                      >
                        {object.locked ? (
                          <Lock size={13} />
                        ) : (
                          <Unlock size={13} />
                        )}
                      </button>
                    </div>
                  </div>
                )
              })}
            </div>
            {selected && selectedIDs.size === 1 && (
              <div className="editor-properties">
                <label className="editor-layer-name">
                  图层名称
                  <input
                    aria-label="图层名称"
                    type="text"
                    maxLength={64}
                    value={selected.name ?? ''}
                    disabled={operationRunning}
                    onFocus={beginContinuousEdit}
                    onChange={(event) => {
                      const name = event.target.value
                      updateObject(
                        selected.id,
                        (object) => ({
                          ...object,
                          name,
                        }),
                        false,
                      )
                    }}
                    onBlur={finishContinuousEdit}
                  />
                </label>
                {selectedAsset && selectedBounds && (
                  <div className="editor-geometry-grid">
                    <label>
                      <span>中心 X</span>
                      <input
                        aria-label="图层中心 X"
                        type="number"
                        step="1"
                        value={Math.round(selectedBounds.centerX)}
                        disabled={operationRunning || selected.locked}
                        onChange={(event) =>
                          updateSelectedCenter(
                            'x',
                            Number(event.target.value),
                            false,
                          )
                        }
                        onFocus={beginContinuousEdit}
                        onBlur={finishContinuousEdit}
                      />
                    </label>
                    <label>
                      <span>中心 Y</span>
                      <input
                        aria-label="图层中心 Y"
                        type="number"
                        step="1"
                        value={Math.round(selectedBounds.centerY)}
                        disabled={operationRunning || selected.locked}
                        onChange={(event) =>
                          updateSelectedCenter(
                            'y',
                            Number(event.target.value),
                            false,
                          )
                        }
                        onFocus={beginContinuousEdit}
                        onBlur={finishContinuousEdit}
                      />
                    </label>
                    <label>
                      <span>宽度</span>
                      <input
                        aria-label="图层宽度"
                        type="number"
                        min="1"
                        step="1"
                        value={Math.round(
                          selectedAsset.width * (selectedAxisScales?.x ?? 1),
                        )}
                        disabled={operationRunning || selected.locked}
                        onChange={(event) =>
                          updateSelectedSize(
                            'width',
                            Number(event.target.value),
                            false,
                          )
                        }
                        onFocus={beginContinuousEdit}
                        onBlur={finishContinuousEdit}
                      />
                    </label>
                    <label>
                      <span>高度</span>
                      <input
                        aria-label="图层高度"
                        type="number"
                        min="1"
                        step="1"
                        value={Math.round(
                          selectedAsset.height * (selectedAxisScales?.y ?? 1),
                        )}
                        disabled={operationRunning || selected.locked}
                        onChange={(event) =>
                          updateSelectedSize(
                            'height',
                            Number(event.target.value),
                            false,
                          )
                        }
                        onFocus={beginContinuousEdit}
                        onBlur={finishContinuousEdit}
                      />
                    </label>
                  </div>
                )}
                <div className="editor-align-actions" aria-label="图层对齐">
                  <button
                    type="button"
                    title="水平居中到画板"
                    disabled={
                      operationRunning || selected.locked || !selectedAsset
                    }
                    onClick={() =>
                      updateSelectedCenter('x', documentState.canvas.width / 2)
                    }
                  >
                    <AlignCenterVertical size={15} />
                  </button>
                  <button
                    type="button"
                    title="垂直居中到画板"
                    disabled={
                      operationRunning || selected.locked || !selectedAsset
                    }
                    onClick={() =>
                      updateSelectedCenter('y', documentState.canvas.height / 2)
                    }
                  >
                    <AlignCenterHorizontal size={15} />
                  </button>
                  <button
                    type="button"
                    title="复制图层"
                    disabled={operationRunning}
                    onClick={duplicateSelectedObjects}
                  >
                    <Copy size={15} />
                  </button>
                </div>
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
                      updateObject(
                        selected.id,
                        (object) =>
                          scaleAroundCenter(
                            object,
                            selectedAsset.width,
                            selectedAsset.height,
                            Number(event.target.value),
                          ),
                        false,
                      )
                    }}
                    onPointerDown={beginContinuousEdit}
                    onPointerUp={finishContinuousEdit}
                    onPointerCancel={finishContinuousEdit}
                    onKeyDown={beginContinuousEdit}
                    onKeyUp={finishContinuousEdit}
                    onBlur={finishContinuousEdit}
                  />
                </label>
                <button
                  className="editor-remove-layer"
                  type="button"
                  disabled={
                    operationRunning ||
                    documentState.objects.length <= 1 ||
                    !selectedObjects.some(
                      (item) =>
                        item.asset_id !== projectQuery.data?.source_asset_id,
                    )
                  }
                  onClick={removeSelectedObjects}
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
                      updateObject(
                        selected.id,
                        (object) =>
                          rotateAroundCenter(
                            object,
                            selectedAsset.width,
                            selectedAsset.height,
                            Number(event.target.value) -
                              objectRotation(object.transform),
                          ),
                        false,
                      )
                    }}
                    onPointerDown={beginContinuousEdit}
                    onPointerUp={finishContinuousEdit}
                    onPointerCancel={finishContinuousEdit}
                    onKeyDown={beginContinuousEdit}
                    onKeyUp={finishContinuousEdit}
                    onBlur={finishContinuousEdit}
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
                      updateObject(
                        selected.id,
                        (object) => ({
                          ...object,
                          opacity: Number(event.target.value),
                        }),
                        false,
                      )
                    }
                    onPointerDown={beginContinuousEdit}
                    onPointerUp={finishContinuousEdit}
                    onPointerCancel={finishContinuousEdit}
                    onKeyDown={beginContinuousEdit}
                    onKeyUp={finishContinuousEdit}
                    onBlur={finishContinuousEdit}
                  />
                </label>
                {selected.crop && (
                  <div className="editor-crop-summary">
                    <span>
                      裁切区域{' '}
                      <strong>
                        {Math.round(selected.crop.width * 100)} ×{' '}
                        {Math.round(selected.crop.height * 100)}%
                      </strong>
                    </span>
                    <button
                      type="button"
                      disabled={operationRunning || selected.locked}
                      onClick={() =>
                        updateObject(selected.id, (object) => ({
                          ...object,
                          crop: undefined,
                        }))
                      }
                    >
                      清除裁切
                    </button>
                  </div>
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
                      onClick={() => {
                        if (currentLayerSet)
                          void operations.publishLayer(
                            currentLayerSet.id,
                            selectedLayer.id,
                          )
                      }}
                    >
                      保存为图片
                    </button>
                  </div>
                )}
                {currentLayerSet && (
                  <button
                    className="editor-package-layers"
                    type="button"
                    disabled={operations.packageRunning}
                    onClick={() => void operations.packageLayers()}
                  >
                    {operations.packageRunning ? '正在打包' : '下载全部图层'}
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

async function saveEditorDocument(
  url: string,
  expectedRevision: number,
  document: EditorDocument | EditorDocumentV2,
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

function translateObject(
  object: EditorObject,
  initial: EditorObject['transform'],
  dx: number,
  dy: number,
): EditorObject {
  return {
    ...object,
    transform: [
      initial[0],
      initial[1],
      initial[2],
      initial[3],
      initial[4] + dx,
      initial[5] + dy,
    ],
  }
}

function roundedCrop(crop: CropRect): CropRect {
  return {
    x: Math.round(crop.x * 100_000) / 100_000,
    y: Math.round(crop.y * 100_000) / 100_000,
    width: Math.round(crop.width * 100_000) / 100_000,
    height: Math.round(crop.height * 100_000) / 100_000,
  }
}

function isFullCrop(crop: CropRect) {
  return (
    Math.abs(crop.x) < 1e-5 &&
    Math.abs(crop.y) < 1e-5 &&
    Math.abs(crop.width - 1) < 1e-5 &&
    Math.abs(crop.height - 1) < 1e-5
  )
}

function sameCrop(left?: CropRect, right?: CropRect) {
  if (!left || !right) return left === right
  return (
    Math.abs(left.x - right.x) < 1e-5 &&
    Math.abs(left.y - right.y) < 1e-5 &&
    Math.abs(left.width - right.width) < 1e-5 &&
    Math.abs(left.height - right.height) < 1e-5
  )
}

function boundsFromPoints(
  start: { x: number; y: number },
  end: { x: number; y: number },
) {
  const left = Math.min(start.x, end.x)
  const top = Math.min(start.y, end.y)
  const right = Math.max(start.x, end.x)
  const bottom = Math.max(start.y, end.y)
  return {
    left,
    top,
    right,
    bottom,
    centerX: (left + right) / 2,
    centerY: (top + bottom) / 2,
    width: right - left,
    height: bottom - top,
  }
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
