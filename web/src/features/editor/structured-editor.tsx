import { useQuery } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Eye,
  EyeOff,
  FolderMinus,
  FolderPlus,
  Layers,
  Link,
  Link2Off,
  Lock,
  Maximize,
  Redo2,
  Save,
  Sparkles,
  Undo2,
  Unlock,
} from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, DragEvent as ReactDragEvent } from 'react'

import { AppShell } from '#/components/app-shell'
import { ConfirmDialog } from '#/components/confirm-dialog'
import { api, APIError } from '#/lib/api'
import type { Asset, EditorProject, LayerSet } from '#/lib/api'
import { fitArtboard } from '#/lib/editor-transform'
import { EditorOperationWaiting } from './editor-operation-waiting'
import {
  attachEditorMask,
  detachEditorMask,
  EditorCommandError,
  groupEditorNodes,
  ungroupEditorNode,
} from './domain/authoring-v2'
import type { EditorDocumentV2, EditorNodeV2 } from './domain/document-v2'
import { EditorHistoryV2 } from './domain/history-v2'
import {
  buildVisibleEditorLayerRows,
  canAttachEditorMask,
  canGroupEditorNodes,
  moveEditorNodesByDrop,
  reorderEditorNodeRelative,
} from './domain/layer-panel-model'
import type { EditorLayerDropPosition } from './domain/layer-panel-model'
import { PixiSurface } from './renderer/pixi-surface'
import { StructuredCanvasInteraction } from './structured-canvas-interaction'
import { useEditorOperations } from './use-editor-operations'
import type { LayerDecompositionSettings } from './use-editor-operations'

type SaveState =
  'saved' | 'dirty' | 'saving' | 'offline' | 'conflict' | 'invalid'

type StructuredEditorProps = {
  project: EditorProject & { document: EditorDocumentV2 }
  onBack: () => void
  onProjectChange: (project: EditorProject) => void
}

export function StructuredEditor({
  project,
  onBack,
  onProjectChange,
}: StructuredEditorProps) {
  const [document, setDocument] = useState(() =>
    structuredClone(project.document),
  )
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(new Set())
  const [activeID, setActiveID] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const [saveState, setSaveState] = useState<SaveState>('saved')
  const [notice, setNotice] = useState('')
  const [view, setView] = useState({ zoom: 100, panX: 0, panY: 0 })
  const [presented, setPresented] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [rerunConfirm, setRerunConfirm] = useState(false)
  const [pendingLayerSet, setPendingLayerSet] = useState<LayerSet>()
  const draggedLayerIDsRef = useRef<string[]>([])
  const [layerDrop, setLayerDrop] = useState<{
    id: string
    position: EditorLayerDropPosition
  }>()
  const [settings, setSettings] = useState<LayerDecompositionSettings>({
    prompt: '',
    resolution: 'auto',
    mode: 'standard',
  })
  const viewportRef = useRef<HTMLDivElement>(null)
  const documentRef = useRef(document)
  const historyRef = useRef(new EditorHistoryV2(100))
  const revisionRef = useRef(project.revision)
  const saveTimerRef = useRef<number | undefined>(undefined)
  const saveTailRef = useRef<Promise<void> | null>(null)
  const dirtyRef = useRef(false)
  const saveBlockedRef = useRef(false)
  const [, setHistoryRevision] = useState(0)

  const rows = useMemo(
    () => buildVisibleEditorLayerRows(document, collapsed),
    [document, collapsed],
  )
  const activeNode = document.nodes.find((node) => node.id === activeID)
  const selectedNodes = document.nodes.filter((node) =>
    selectedIDs.has(node.id),
  )
  const assetIDs = useMemo(
    () =>
      [
        ...new Set(
          document.nodes.flatMap((node) =>
            node.type === 'raster' && node.asset_id ? [node.asset_id] : [],
          ),
        ),
      ].sort(),
    [document],
  )
  const assetsQuery = useQuery({
    queryKey: ['editor-assets-resolved', ...assetIDs],
    enabled: assetIDs.length > 0,
    queryFn: () =>
      api<{ items: Asset[] }>('/api/v1/assets/resolve', {
        method: 'POST',
        body: JSON.stringify({ asset_ids: assetIDs }),
      }),
  })
  const assets = useMemo(
    () =>
      new Map(
        (assetsQuery.data?.items ?? []).map((asset) => [asset.id, asset]),
      ),
    [assetsQuery.data?.items],
  )
  const saveNow = useCallback(async () => {
    if (!dirtyRef.current) return
    if (saveBlockedRef.current)
      throw new Error('structured editor save is blocked')
    if (saveTailRef.current) return saveTailRef.current
    const snapshot = structuredClone(documentRef.current)
    const signature = JSON.stringify(snapshot)
    setSaveState('saving')
    const task = saveStructuredDocument(
      project.id,
      revisionRef.current,
      snapshot,
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
        onProjectChange({
          ...project,
          document: snapshot,
          revision: result.revision,
        })
      })
      .catch((error: unknown) => {
        if (error instanceof APIError && error.status === 409) {
          saveBlockedRef.current = true
          setSaveState('conflict')
        } else if (error instanceof APIError && error.status === 422) {
          saveBlockedRef.current = true
          setSaveState('invalid')
        } else setSaveState('offline')
        throw error
      })
      .finally(() => {
        saveTailRef.current = null
      })
    saveTailRef.current = task
    return task
  }, [onProjectChange, project])

  const scheduleSave = useCallback(() => {
    dirtyRef.current = true
    if (!saveBlockedRef.current) setSaveState('dirty')
    window.clearTimeout(saveTimerRef.current)
    if (saveBlockedRef.current) return
    saveTimerRef.current = window.setTimeout(
      () => void saveNow().catch(() => undefined),
      1_000,
    )
  }, [saveNow])

  function applyDocument(
    next: EditorDocumentV2,
    options: { remember?: boolean; mergeKey?: string } = {},
  ) {
    if (operationsRef.current) return false
    if (JSON.stringify(documentRef.current) === JSON.stringify(next))
      return false
    if (
      options.remember !== false &&
      historyRef.current.commit(documentRef.current, next, {
        mergeKey: options.mergeKey,
      })
    )
      setHistoryRevision((value) => value + 1)
    documentRef.current = next
    setDocument(next)
    scheduleSave()
    return true
  }

  function previewDocument(next: EditorDocumentV2) {
    if (operationsRef.current) return
    documentRef.current = next
    setDocument(next)
  }

  function commitCanvasDocument(
    initial: EditorDocumentV2,
    next: EditorDocumentV2,
    mergeKey?: string,
  ) {
    if (operationsRef.current) return
    documentRef.current = next
    setDocument(next)
    if (historyRef.current.commit(initial, next, { mergeKey }))
      setHistoryRevision((value) => value + 1)
    scheduleSave()
  }

  const operationsRef = useRef(false)

  function runCommand(command: () => EditorDocumentV2, message: string) {
    try {
      const changed = applyDocument(command())
      if (changed) setNotice(message)
      return changed
    } catch (error) {
      setNotice(commandErrorMessage(error))
      return false
    }
  }

  function updateNode(
    id: string,
    update: (node: EditorNodeV2) => EditorNodeV2,
    mergeKey?: string,
  ) {
    applyDocument(
      {
        ...documentRef.current,
        nodes: documentRef.current.nodes.map((node) =>
          node.id === id ? update(node) : node,
        ),
      },
      { mergeKey },
    )
  }

  function selectNode(id: string, additive = false) {
    const next = additive ? new Set(selectedIDs) : new Set<string>()
    if (additive && next.has(id)) next.delete(id)
    else next.add(id)
    setSelectedIDs(next)
    setActiveID(next.has(id) ? id : ([...next].at(-1) ?? ''))
  }

  function layerDropPosition(
    event: ReactDragEvent<HTMLElement>,
    node: EditorNodeV2,
  ): EditorLayerDropPosition {
    const bounds = event.currentTarget.getBoundingClientRect()
    const ratio = (event.clientY - bounds.top) / Math.max(1, bounds.height)
    if (node.type === 'group' && ratio >= 0.25 && ratio <= 0.75) return 'inside'
    return ratio < 0.5 ? 'before' : 'after'
  }

  function groupSelection() {
    const id = crypto.randomUUID()
    const changed = runCommand(
      () =>
        groupEditorNodes(documentRef.current, [...selectedIDs], {
          id,
          name: '新建组',
        }),
      '已创建图层组',
    )
    if (changed) {
      setSelectedIDs(new Set([id]))
      setActiveID(id)
    }
  }

  function attachMask() {
    if (selectedNodes.length !== 2 || !activeNode) return
    const mask = selectedNodes.find((node) => node.id !== activeNode.id)
    if (!mask) return
    const changed = runCommand(
      () => attachEditorMask(documentRef.current, activeNode.id, mask.id),
      '已将所选图层设为蒙版',
    )
    if (changed) setSelectedIDs(new Set([activeNode.id]))
  }

  function undo() {
    if (!historyRef.current.canUndo) return
    const next = historyRef.current.undo(documentRef.current)
    documentRef.current = next
    setDocument(next)
    setHistoryRevision((value) => value + 1)
    scheduleSave()
  }

  function redo() {
    if (!historyRef.current.canRedo) return
    const next = historyRef.current.redo(documentRef.current)
    documentRef.current = next
    setDocument(next)
    setHistoryRevision((value) => value + 1)
    scheduleSave()
  }

  const fitCanvas = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    setView(
      fitArtboard(
        viewport.clientWidth,
        viewport.clientHeight,
        document.canvas.width,
        document.canvas.height,
      ),
    )
  }, [document.canvas.height, document.canvas.width])

  useEffect(() => {
    requestAnimationFrame(fitCanvas)
  }, [fitCanvas])

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

  async function leave() {
    try {
      await Promise.race([
        flushSaves(),
        new Promise((_, reject) =>
          window.setTimeout(() => reject(new Error('save timeout')), 3_000),
        ),
      ])
    } catch {
      setNotice('工程尚未保存，请先重试保存')
      return
    }
    onBack()
  }

  async function flushSaves() {
    window.clearTimeout(saveTimerRef.current)
    while (dirtyRef.current) {
      if (saveBlockedRef.current)
        throw new Error('structured editor save is blocked')
      if (saveTailRef.current) await saveTailRef.current
      else await saveNow()
    }
  }

  function buildLayerSetDocument(layerSet: LayerSet): EditorDocumentV2 {
    return {
      ...documentRef.current,
      nodes: [
        {
          id: `base-${layerSet.id}`,
          type: 'raster',
          name: '背景',
          parent_id: null,
          order_key: '00000000',
          transform: [1, 0, 0, 1, 0, 0],
          opacity: 1,
          blend_mode: 'normal',
          visible: true,
          locked: false,
          asset_id: layerSet.base_asset.id,
        },
        ...layerSet.items.map<EditorNodeV2>((item, index) => {
          const [left, top, right, bottom] = item.bounding_box_absolute
          return {
            id: item.id,
            type: 'raster',
            name: item.name || `图层 ${item.z_index}`,
            parent_id: null,
            order_key: (index + 1).toString().padStart(8, '0'),
            transform: [
              (right - left) / item.asset.width,
              0,
              0,
              (bottom - top) / item.asset.height,
              left,
              top,
            ],
            opacity: 1,
            blend_mode: 'normal',
            visible: true,
            locked: false,
            asset_id: item.asset.id,
          }
        }),
      ],
    }
  }

  function applyLayerSet(layerSet: LayerSet) {
    const next = buildLayerSetDocument(layerSet)
    if (!applyDocument(next)) return
    const selected = next.nodes.at(-1)?.id ?? ''
    setActiveID(selected)
    setSelectedIDs(new Set(selected ? [selected] : []))
    setPendingLayerSet(undefined)
    setNotice(`已整理 ${next.nodes.length} 个透明图层`)
  }

  const operations = useEditorOperations({
    projectID: project.id,
    initialOperationID: project.latest_operation_id,
    activeLayerSet: project.active_layer_set,
    getRevision: () => revisionRef.current,
    flushSaves,
    onLayerSetReady: (layerSet, sourceRevision) => {
      if (sourceRevision === revisionRef.current && layerSet.applied_to_project)
        applyLayerSet(layerSet)
      else if (!layerSet.applied_to_project) setPendingLayerSet(layerSet)
    },
    onNotice: setNotice,
  })
  operationsRef.current = operations.running

  async function requestDecomposition(confirmed = false) {
    if (operations.currentLayerSet && !confirmed) {
      setRerunConfirm(true)
      return
    }
    if (await operations.startDecomposition(settings)) setSettingsOpen(false)
  }

  function downloadDocument() {
    const blob = new Blob([JSON.stringify(documentRef.current, null, 2)], {
      type: 'application/json',
    })
    const href = URL.createObjectURL(blob)
    const link = window.document.createElement('a')
    link.href = href
    link.download = `${project.name || 'cornfield-editor'}.json`
    link.click()
    URL.revokeObjectURL(href)
  }

  return (
    <AppShell immersive>
      <main className="image-editor structured-editor">
        <header className="editor-topbar">
          <div className="editor-topbar-group">
            <button
              type="button"
              aria-label="返回工作区"
              onClick={() => void leave()}
            >
              <ArrowLeft size={17} />
            </button>
            <strong className="structured-project-name">{project.name}</strong>
            <span className={`editor-save-state is-${saveState}`}>
              {saveStateLabel(saveState)}
            </span>
            {saveState === 'offline' && (
              <button type="button" onClick={() => void saveNow()}>
                重试保存
              </button>
            )}
          </div>
          <div className="editor-topbar-group">
            <button
              type="button"
              aria-label="撤销"
              disabled={!historyRef.current.canUndo}
              onClick={undo}
            >
              <Undo2 size={16} />
            </button>
            <button
              type="button"
              aria-label="重做"
              disabled={!historyRef.current.canRedo}
              onClick={redo}
            >
              <Redo2 size={16} />
            </button>
          </div>
          <div className="editor-topbar-group structured-actions">
            <button
              type="button"
              disabled={
                operations.running || !canGroupEditorNodes(selectedNodes)
              }
              onClick={groupSelection}
            >
              <FolderPlus size={16} /> 成组
            </button>
            <button
              type="button"
              disabled={operations.running || activeNode?.type !== 'group'}
              onClick={() => {
                if (!activeNode) return
                const childIDs = document.nodes
                  .filter((node) => node.parent_id === activeNode.id)
                  .map((node) => node.id)
                const changed = runCommand(
                  () => ungroupEditorNode(documentRef.current, activeNode.id),
                  '已解散图层组',
                )
                if (changed) {
                  setSelectedIDs(new Set(childIDs))
                  setActiveID(childIDs.at(-1) ?? '')
                }
              }}
            >
              <FolderMinus size={16} /> 解组
            </button>
            <button
              type="button"
              disabled={
                operations.running ||
                !canAttachEditorMask(selectedNodes, activeNode)
              }
              onClick={attachMask}
            >
              <Link size={16} /> 设为蒙版
            </button>
            <button
              type="button"
              disabled={operations.running || !activeNode?.mask_id}
              onClick={() => {
                if (!activeNode) return
                runCommand(
                  () => detachEditorMask(documentRef.current, activeNode.id),
                  '已解除蒙版',
                )
              }}
            >
              <Link2Off size={16} /> 解除蒙版
            </button>
            <button
              type="button"
              className="editor-layer-settings"
              aria-label="智能分层设置"
              disabled={operations.running}
              onClick={() => setSettingsOpen((value) => !value)}
            >
              <Layers size={16} /> 参数
            </button>
            <button
              type="button"
              className="editor-decompose"
              disabled={operations.running || !operations.canDecompose}
              title={operations.capabilityMessage || '智能分层'}
              onClick={() => void requestDecomposition()}
            >
              <Sparkles size={16} /> 智能分层
            </button>
            <button
              type="button"
              className="editor-publish"
              disabled={operations.running}
              onClick={() => void operations.publish()}
            >
              <Save size={16} /> 保存为新图片
            </button>
          </div>
        </header>

        <section className="structured-editor-body">
          <div className="structured-canvas" ref={viewportRef}>
            <PixiSurface
              enabled
              document={document}
              assets={assets}
              viewport={view}
              onUnavailable={(reason) => setNotice(`图形渲染不可用：${reason}`)}
              onPresentedChange={setPresented}
            />
            <StructuredCanvasInteraction
              document={document}
              assets={assets}
              view={view}
              selectedIDs={selectedIDs}
              disabled={operations.running}
              onViewChange={setView}
              onSelectionChange={(ids, active) => {
                setSelectedIDs(new Set(ids))
                setActiveID(active)
              }}
              onPreview={previewDocument}
              onCommit={commitCanvasDocument}
              onFit={fitCanvas}
            />
            {assetsQuery.isError ? (
              <div className="structured-render-wait" role="alert">
                工程资源无法读取，请刷新后重试
              </div>
            ) : !presented ? (
              <div className="structured-render-wait" aria-live="polite">
                <span className="spinner" /> 正在准备专业画布
              </div>
            ) : null}
            {operations.running && (
              <EditorOperationWaiting
                status={operations.operation?.status}
                message={operations.operation?.message}
                elapsed={operations.elapsed}
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

          <aside className="structured-layer-panel">
            <header>
              <div>
                <strong>图层</strong>
                <span>{document.nodes.length} / 500</span>
              </div>
              <button type="button" title="适应画布" onClick={fitCanvas}>
                <Maximize size={15} />
              </button>
            </header>
            <div
              className="structured-layer-tree"
              role="tree"
              aria-label="图层结构"
            >
              {rows.map(({ entry, hasChildren }) => {
                const node = entry.node
                const asset = node.asset_id
                  ? assets.get(node.asset_id)
                  : undefined
                const isCollapsed = collapsed.has(node.id)
                return (
                  <div
                    key={node.id}
                    className={`structured-layer-row${selectedIDs.has(node.id) ? ' active' : ''}`}
                    data-drop-position={
                      layerDrop?.id === node.id ? layerDrop.position : undefined
                    }
                    style={{ '--layer-depth': entry.depth } as CSSProperties}
                    role="treeitem"
                    aria-level={entry.depth + 1}
                    aria-selected={selectedIDs.has(node.id)}
                    onDragOver={(event) => {
                      if (!draggedLayerIDsRef.current.length) return
                      event.preventDefault()
                      event.dataTransfer.dropEffect = 'move'
                      setLayerDrop({
                        id: node.id,
                        position: layerDropPosition(event, node),
                      })
                    }}
                    onDragLeave={(event) => {
                      if (
                        event.currentTarget.contains(
                          event.relatedTarget as Node,
                        )
                      )
                        return
                      setLayerDrop((current) =>
                        current?.id === node.id ? undefined : current,
                      )
                    }}
                    onDrop={(event) => {
                      event.preventDefault()
                      const position = layerDropPosition(event, node)
                      const ids = draggedLayerIDsRef.current
                      setLayerDrop(undefined)
                      draggedLayerIDsRef.current = []
                      if (!ids.length) return
                      runCommand(
                        () =>
                          moveEditorNodesByDrop(
                            documentRef.current,
                            ids,
                            node.id,
                            position,
                          ),
                        position === 'inside'
                          ? '已移入图层组'
                          : '已调整图层顺序',
                      )
                    }}
                    onDragEnd={() => {
                      setLayerDrop(undefined)
                      draggedLayerIDsRef.current = []
                    }}
                  >
                    <button
                      className="structured-layer-disclosure"
                      type="button"
                      aria-label={isCollapsed ? '展开图层组' : '折叠图层组'}
                      disabled={!hasChildren}
                      onClick={() =>
                        setCollapsed((current) => toggleSet(current, node.id))
                      }
                    >
                      {hasChildren ? (
                        isCollapsed ? (
                          <ChevronRight size={14} />
                        ) : (
                          <ChevronDown size={14} />
                        )
                      ) : null}
                    </button>
                    <button
                      className="structured-layer-main"
                      type="button"
                      draggable={!operations.running}
                      onDragStart={(event) => {
                        const ids = selectedIDs.has(node.id)
                          ? [...selectedIDs]
                          : [node.id]
                        draggedLayerIDsRef.current = ids
                        if (!selectedIDs.has(node.id)) {
                          setSelectedIDs(new Set(ids))
                          setActiveID(node.id)
                        }
                        event.dataTransfer.effectAllowed = 'move'
                        event.dataTransfer.setData(
                          'application/x-cornfield-editor-layers',
                          ids.join(','),
                        )
                      }}
                      onClick={(event) => selectNode(node.id, event.shiftKey)}
                    >
                      <span className="structured-layer-thumb">
                        {node.type === 'group' ? (
                          <Layers size={15} />
                        ) : asset ? (
                          <img src={asset.thumb_320_url} alt="" />
                        ) : null}
                      </span>
                      <span>
                        <strong>
                          {node.name ||
                            (node.type === 'group' ? '图层组' : '图层')}
                        </strong>
                        <small>
                          {node.mask_id
                            ? '含蒙版'
                            : node.type === 'group'
                              ? '组'
                              : '像素图层'}
                        </small>
                      </span>
                    </button>
                    <div className="structured-layer-controls">
                      <button
                        type="button"
                        disabled={operations.running}
                        aria-label={node.visible ? '隐藏图层' : '显示图层'}
                        onClick={() =>
                          updateNode(node.id, (value) => ({
                            ...value,
                            visible: !value.visible,
                          }))
                        }
                      >
                        {node.visible ? (
                          <Eye size={13} />
                        ) : (
                          <EyeOff size={13} />
                        )}
                      </button>
                      <button
                        type="button"
                        disabled={operations.running}
                        aria-label={node.locked ? '解锁图层' : '锁定图层'}
                        onClick={() =>
                          updateNode(node.id, (value) => ({
                            ...value,
                            locked: !value.locked,
                          }))
                        }
                      >
                        {node.locked ? (
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

            {activeNode && (
              <section className="structured-properties">
                <label>
                  图层名称
                  <input
                    value={activeNode.name ?? ''}
                    maxLength={64}
                    disabled={operations.running}
                    onChange={(event) =>
                      updateNode(
                        activeNode.id,
                        (node) => ({
                          ...node,
                          name: event.target.value,
                        }),
                        `name:${activeNode.id}`,
                      )
                    }
                  />
                </label>
                <label>
                  透明度{' '}
                  <output>{Math.round(activeNode.opacity * 100)}%</output>
                  <input
                    type="range"
                    min="0"
                    max="1"
                    step="0.01"
                    value={activeNode.opacity}
                    disabled={operations.running}
                    onChange={(event) =>
                      updateNode(
                        activeNode.id,
                        (node) => ({
                          ...node,
                          opacity: Number(event.target.value),
                        }),
                        `opacity:${activeNode.id}`,
                      )
                    }
                  />
                </label>
                <div className="structured-order-controls">
                  <button
                    type="button"
                    disabled={operations.running}
                    onClick={() =>
                      runCommand(
                        () =>
                          reorderEditorNodeRelative(
                            documentRef.current,
                            activeNode.id,
                            1,
                          ),
                        '已上移图层',
                      )
                    }
                  >
                    <ArrowUp size={14} /> 上移
                  </button>
                  <button
                    type="button"
                    disabled={operations.running}
                    onClick={() =>
                      runCommand(
                        () =>
                          reorderEditorNodeRelative(
                            documentRef.current,
                            activeNode.id,
                            -1,
                          ),
                        '已下移图层',
                      )
                    }
                  >
                    <ArrowDown size={14} /> 下移
                  </button>
                </div>
                {operations.currentLayerSet?.items.some(
                  (item) => item.id === activeNode.id,
                ) && (
                  <button
                    className="editor-package-layers"
                    type="button"
                    onClick={() => {
                      const layer = operations.currentLayerSet?.items.find(
                        (item) => item.id === activeNode.id,
                      )
                      if (layer && operations.currentLayerSet)
                        void operations.publishLayer(
                          operations.currentLayerSet.id,
                          layer.id,
                        )
                    }}
                  >
                    保存当前图层为图片
                  </button>
                )}
                {operations.currentLayerSet && (
                  <button
                    className="editor-package-layers"
                    type="button"
                    disabled={operations.packageRunning}
                    onClick={() => void operations.packageLayers()}
                  >
                    {operations.packageRunning ? '正在打包' : '下载全部图层'}
                  </button>
                )}
              </section>
            )}
          </aside>
        </section>

        <footer className="editor-statusbar">
          <span>{notice || 'V2 专业图层模式'}</span>
          <div>
            <span>{Math.round(view.zoom)}%</span>
            <input
              aria-label="画布缩放"
              type="range"
              min="10"
              max="400"
              value={view.zoom}
              onChange={(event) =>
                setView((current) => ({
                  ...current,
                  zoom: Number(event.target.value),
                }))
              }
            />
          </div>
        </footer>
        {(saveState === 'conflict' || saveState === 'invalid') && (
          <section className="editor-save-recovery" role="alert">
            <strong>
              {saveState === 'conflict'
                ? '工程已在其他窗口更新'
                : '自动保存已暂停'}
            </strong>
            <span>
              当前画面仍保留在本机。请先下载工程备份，再重新载入云端版本。
            </span>
            <div>
              <button type="button" onClick={downloadDocument}>
                下载工程 JSON
              </button>
              <button type="button" onClick={() => window.location.reload()}>
                重新载入
              </button>
            </div>
          </section>
        )}
        {settingsOpen && (
          <aside className="editor-layer-drawer" aria-label="智能分层设置">
            <button
              type="button"
              className="drawer-close"
              aria-label="关闭设置"
              onClick={() => setSettingsOpen(false)}
            >
              ×
            </button>
            <p className="eyebrow">LAYER DECOMPOSITION</p>
            <h2>智能分层</h2>
            <label>
              指定元素（可选）
              <textarea
                value={settings.prompt}
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
                      .value as LayerDecompositionSettings['resolution'],
                  }))
                }
              >
                {['auto', '1K', '1.5K', '2K'].map((value) => (
                  <option key={value}>{value}</option>
                ))}
              </select>
            </label>
            <label>
              提示词优化
              <select
                value={settings.mode}
                onChange={(event) =>
                  setSettings((current) => ({
                    ...current,
                    mode: event.target
                      .value as LayerDecompositionSettings['mode'],
                  }))
                }
              >
                <option value="standard">标准</option>
                <option value="fast">快速</option>
              </select>
            </label>
            <button
              className="editor-decompose"
              type="button"
              onClick={() => void requestDecomposition()}
            >
              <Sparkles size={16} /> 开始分层
            </button>
          </aside>
        )}
        <ConfirmDialog
          open={rerunConfirm}
          title="重新智能分层"
          description="会产生一次新的付费请求；旧图层在新结果成功前仍会保留。"
          confirmLabel="继续分层"
          onCancel={() => setRerunConfirm(false)}
          onConfirm={() => {
            setRerunConfirm(false)
            void requestDecomposition(true)
          }}
        />
      </main>
    </AppShell>
  )
}

function toggleSet(values: ReadonlySet<string>, value: string) {
  const next = new Set(values)
  if (next.has(value)) next.delete(value)
  else next.add(value)
  return next
}

function commandErrorMessage(error: unknown) {
  if (!(error instanceof EditorCommandError)) return '图层操作失败，请重试'
  return {
    INVALID_DOCUMENT: '工程结构无效，已停止修改',
    INVALID_SELECTION: '请选择同一层级且关系完整的图层',
    INVALID_PARENT: '目标图层组无法保持当前画面',
    INVALID_MASK: '蒙版必须是未裁切、未被占用的同级像素图层',
    CYCLE: '图层组不能移动到自己的子级中',
  }[error.code]
}

async function saveStructuredDocument(
  projectID: string,
  expectedRevision: number,
  document: EditorDocumentV2,
) {
  for (let attempt = 0; ; attempt++) {
    try {
      return await api<{ revision: number }>(
        `/api/v1/editor-projects/${projectID}/document`,
        {
          method: 'PUT',
          body: JSON.stringify({
            expected_revision: expectedRevision,
            document,
          }),
        },
      )
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
