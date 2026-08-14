import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import type { Asset } from '#/lib/api'
import { APIError } from '#/lib/api'
import type { EditorTransform } from '../../domain/document'
import type { EditorDocumentV2, EditorNodeV2 } from '../../domain/document-v2'
import type { EditorRasterMaskRenderResource } from '../../renderer/types'
import {
  commitRasterMaskVersion,
  createRasterMaskResource,
  loadRasterMaskTiles,
  loadRasterMaskVersion,
} from './persistence'
import type {
  RasterMaskBrush,
  RasterMaskPoint,
  RasterMaskTileSnapshot,
} from './tile-mask'
import { createRasterMaskWorkerClient } from './worker-client'

export type RasterMaskTool = 'select' | 'brush' | 'eraser'

export type RasterBrushSettings = {
  size: number
  hardness: number
  opacity: number
  pressure: boolean
}

type Inputs = {
  projectID: string
  document: EditorDocumentV2
  activeNode?: EditorNodeV2
  assets: ReadonlyMap<string, Asset>
  getRevision: () => number
  flushDocumentSaves: () => Promise<void>
  onDocumentFromServer: (document: EditorDocumentV2, revision: number) => void
  onNotice: (message: string) => void
}

type Session = {
  nodeID: string
  resourceID: string
  worker: ReturnType<typeof createRasterMaskWorkerClient>
  defaultAlpha: number
  version: number
  width: number
  height: number
  tiles: Map<string, RasterMaskTileSnapshot>
  generation: number
}

type PendingTile = { generation: number; tile: RasterMaskTileSnapshot }

export function useRasterMaskEditor({
  projectID,
  document,
  activeNode,
  assets,
  getRevision,
  flushDocumentSaves,
  onDocumentFromServer,
  onNotice,
}: Inputs) {
  const [tool, setToolState] = useState<RasterMaskTool>('select')
  const [settings, setSettings] = useState<RasterBrushSettings>({
    size: 96,
    hardness: 0.75,
    opacity: 1,
    pressure: true,
  })
  const [resources, setResources] = useState<
    ReadonlyMap<string, EditorRasterMaskRenderResource>
  >(new Map())
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [blocked, setBlocked] = useState(false)
  const blockedRef = useRef(false)
  const [history, setHistory] = useState({ canUndo: false, canRedo: false })
  const sessionRef = useRef<Session | undefined>(undefined)
  const pendingRef = useRef(new Map<string, PendingTile>())
  const persistenceTailRef = useRef<Promise<void> | null>(null)
  const persistenceErrorRef = useRef<Error | undefined>(undefined)
  const persistTimerRef = useRef<number | undefined>(undefined)
  const changeSequenceRef = useRef(0)
  const documentRef = useRef(document)
  documentRef.current = document
  const loadedVersionsRef = useRef(new Set<string>())
  const maskLoadTailRef = useRef(Promise.resolve())
  const persistRef = useRef<() => Promise<void> | undefined>(() => undefined)
  const flushRef = useRef<() => Promise<void>>(async () => undefined)

  const schedulePersist = useCallback((delay = 850) => {
    window.clearTimeout(persistTimerRef.current)
    if (blockedRef.current) return
    persistTimerRef.current = window.setTimeout(
      () => void persistRef.current(),
      delay,
    )
  }, [])

  const activeAsset =
    activeNode?.type === 'raster' && activeNode.asset_id
      ? assets.get(activeNode.asset_id)
      : undefined
  const available = Boolean(
    activeNode?.type === 'raster' &&
    activeAsset &&
    !activeNode.locked &&
    !activeNode.mask_id &&
    !activeNode.shape_mask,
  )

  const maskReferences = useMemo(
    () => [
      ...new Map(
        document.nodes.flatMap((node) =>
          node.type === 'raster' && node.pixel_mask
            ? [
                [
                  `${node.pixel_mask.resource_id}:${node.pixel_mask.version}`,
                  node.pixel_mask,
                ] as const,
              ]
            : [],
        ),
      ).values(),
    ],
    [document.nodes],
  )

  useEffect(() => {
    for (const reference of maskReferences) {
      const key = `${reference.resource_id}:${reference.version}`
      if (loadedVersionsRef.current.has(key)) continue
      loadedVersionsRef.current.add(key)
      maskLoadTailRef.current = maskLoadTailRef.current
        .then(() =>
          loadRasterMaskVersion(
            projectID,
            reference.resource_id,
            reference.version,
          ),
        )
        .then(async (version) => ({
          version,
          tiles: await loadRasterMaskTiles(version, 4),
        }))
        .then(({ version, tiles }) => {
          if (sessionRef.current?.resourceID === version.mask.id) return
          setResources((current) =>
            new Map(current).set(version.mask.id, {
              id: version.mask.id,
              version: version.version,
              width: version.mask.width,
              height: version.mask.height,
              defaultAlpha: version.mask.default_alpha,
              generation: 1,
              tiles,
            }),
          )
        })
        .catch(() => {
          loadedVersionsRef.current.delete(key)
          onNotice('部分像素蒙版暂时无法读取，请刷新后重试')
        })
    }
  }, [maskReferences, onNotice, projectID])

  const publishMutation = useCallback(
    (session: Session, tiles: readonly RasterMaskTileSnapshot[]) => {
      for (const tile of tiles) {
        const key = tileKey(tile)
        if (isDefault(tile.alpha, session.defaultAlpha))
          session.tiles.delete(key)
        else session.tiles.set(key, cloneTile(tile))
      }
      session.generation += 1
      setResources((current) =>
        new Map(current).set(session.resourceID, {
          id: session.resourceID,
          version: session.version,
          width: session.width,
          height: session.height,
          defaultAlpha: session.defaultAlpha,
          generation: session.generation,
          tiles: [...session.tiles.values()],
          changedTiles: tiles.map(cloneTile),
        }),
      )
    },
    [],
  )

  const applyServerVersion = useCallback(
    (session: Session, version: number, projectRevision: number) => {
      session.version = version
      const next: EditorDocumentV2 = {
        ...documentRef.current,
        nodes: documentRef.current.nodes.map((node) =>
          node.id === session.nodeID
            ? {
                ...node,
                pixel_mask: {
                  resource_id: session.resourceID,
                  version,
                },
              }
            : node,
        ),
      }
      documentRef.current = next
      onDocumentFromServer(next, projectRevision)
      setResources((current) => {
        const resource = current.get(session.resourceID)
        if (!resource) return current
        return new Map(current).set(session.resourceID, {
          ...resource,
          version,
        })
      })
    },
    [onDocumentFromServer],
  )

  const persist = useCallback(async () => {
    const session = sessionRef.current
    if (!session || blockedRef.current || pendingRef.current.size === 0) return
    if (persistenceTailRef.current) return persistenceTailRef.current
    const entries = [...pendingRef.current.entries()]
    const expectedVersion = session.version
    persistenceErrorRef.current = undefined
    setSaving(true)
    const task = flushDocumentSaves()
      .then(() =>
        commitRasterMaskVersion({
          projectID,
          maskID: session.resourceID,
          expectedProjectRevision: getRevision(),
          expectedMaskVersion: expectedVersion,
          defaultAlpha: session.defaultAlpha,
          tiles: entries.map(([, value]) => value.tile),
        }),
      )
      .then((result) => {
        for (const [key, value] of entries)
          if (pendingRef.current.get(key)?.generation === value.generation)
            pendingRef.current.delete(key)
        applyServerVersion(
          session,
          result.current_version,
          result.project_revision,
        )
        if (pendingRef.current.size > 0) schedulePersist(100)
      })
      .catch((error: unknown) => {
        persistenceErrorRef.current =
          error instanceof Error ? error : new Error('蒙版暂未保存')
        if (error instanceof APIError && [409, 422].includes(error.status)) {
          blockedRef.current = true
          setBlocked(true)
          onNotice(
            error.status === 409
              ? '蒙版版本已在其他页面更新，自动保存已暂停'
              : '蒙版数据无法保存，请下载工程并重新进入',
          )
          return
        }
        onNotice('蒙版暂未保存，网络恢复后请重试')
        schedulePersist(2_000)
      })
      .finally(() => {
        persistenceTailRef.current = null
        setSaving(false)
      })
    persistenceTailRef.current = task
    return task
  }, [applyServerVersion, flushDocumentSaves, getRevision, onNotice, projectID])
  persistRef.current = persist

  function stageTiles(tiles: readonly RasterMaskTileSnapshot[]) {
    for (const tile of tiles) {
      changeSequenceRef.current += 1
      pendingRef.current.set(tileKey(tile), {
        generation: changeSequenceRef.current,
        tile: cloneTile(tile),
      })
    }
    schedulePersist()
  }

  const activate = useCallback(
    async (nextTool: RasterMaskTool) => {
      if (nextTool === 'select') {
        setLoading(true)
        try {
          await flushRef.current()
          setToolState('select')
        } catch (error) {
          onNotice(error instanceof Error ? error.message : '请先完成蒙版保存')
        } finally {
          setLoading(false)
        }
        return
      }
      if (!available || !activeNode || !activeAsset) {
        onNotice('请选择一个未锁定的图片图层后再使用蒙版画笔')
        return
      }
      setLoading(true)
      blockedRef.current = false
      setBlocked(false)
      try {
        if (sessionRef.current && sessionRef.current.nodeID !== activeNode.id) {
          await flushRef.current()
          sessionRef.current.worker.close()
          sessionRef.current = undefined
          pendingRef.current.clear()
        }
        await flushDocumentSaves()
        let reference = activeNode.pixel_mask
        if (!reference) {
          const created = await createRasterMaskResource({
            projectID,
            expectedRevision: getRevision(),
            targetNodeID: activeNode.id,
          })
          reference = {
            resource_id: created.id,
            version: created.current_version,
          }
          const next = {
            ...documentRef.current,
            nodes: documentRef.current.nodes.map((node) =>
              node.id === activeNode.id
                ? { ...node, pixel_mask: reference }
                : node,
            ),
          }
          documentRef.current = next
          onDocumentFromServer(next, created.project_revision)
        }
        const current = sessionRef.current
        if (
          !current ||
          current.resourceID !== reference.resource_id ||
          current.version !== reference.version
        ) {
          current?.worker.close()
          const version = await loadRasterMaskVersion(
            projectID,
            reference.resource_id,
            reference.version,
          )
          const tiles = await loadRasterMaskTiles(version)
          const worker = createRasterMaskWorkerClient()
          await worker.create(version.mask.width, version.mask.height, {
            defaultAlpha: version.mask.default_alpha,
          })
          if (tiles.length) await worker.hydrate(tiles.map(cloneTile))
          const session: Session = {
            nodeID: activeNode.id,
            resourceID: version.mask.id,
            worker,
            defaultAlpha: version.mask.default_alpha,
            version: version.version,
            width: version.mask.width,
            height: version.mask.height,
            tiles: new Map(
              tiles.map((tile) => [tileKey(tile), cloneTile(tile)]),
            ),
            generation: 1,
          }
          sessionRef.current = session
          setResources((currentResources) =>
            new Map(currentResources).set(session.resourceID, {
              id: session.resourceID,
              version: session.version,
              width: session.width,
              height: session.height,
              defaultAlpha: session.defaultAlpha,
              generation: session.generation,
              tiles: [...session.tiles.values()],
            }),
          )
          setHistory({ canUndo: false, canRedo: false })
        }
        setToolState(nextTool)
      } catch (error) {
        onNotice(
          error instanceof Error ? error.message : '蒙版工具无法启动，请重试',
        )
        setToolState('select')
      } finally {
        setLoading(false)
      }
    },
    [
      activeAsset,
      activeNode,
      available,
      flushDocumentSaves,
      getRevision,
      onDocumentFromServer,
      onNotice,
      projectID,
    ],
  )

  const brush = useMemo<RasterMaskBrush>(
    () => ({
      size: settings.size,
      hardness: settings.hardness,
      opacity: settings.opacity,
      spacing: 0.18,
      mode: tool === 'eraser' ? 'erase' : 'paint',
      pressureSize: settings.pressure ? 1 : 0,
      pressureOpacity: settings.pressure ? 0.65 : 0,
    }),
    [settings, tool],
  )

  const mutate = useCallback(
    async (
      action: (worker: Session['worker']) => Promise<{
        tiles: RasterMaskTileSnapshot[]
        canUndo: boolean
        canRedo: boolean
      }>,
      persistAfter = false,
    ) => {
      const session = sessionRef.current
      if (!session) throw new Error('蒙版会话尚未准备完成')
      const result = await action(session.worker)
      publishMutation(session, result.tiles)
      setHistory({ canUndo: result.canUndo, canRedo: result.canRedo })
      if (persistAfter) stageTiles(result.tiles)
      return result
    },
    [publishMutation],
  )

  const undo = useCallback(
    () => mutate((worker) => worker.undo(), true),
    [mutate],
  )
  const redo = useCallback(
    () => mutate((worker) => worker.redo(), true),
    [mutate],
  )

  const flush = useCallback(async () => {
    window.clearTimeout(persistTimerRef.current)
    while (pendingRef.current.size > 0) {
      if (blockedRef.current) throw new Error('蒙版自动保存已暂停')
      if (persistenceTailRef.current) await persistenceTailRef.current
      else await persistRef.current()
      if (pendingRef.current.size > 0 && persistenceErrorRef.current)
        throw persistenceErrorRef.current
    }
  }, [])
  flushRef.current = flush

  useEffect(() => {
    if (tool === 'select') return
    if (!activeNode || activeNode.id !== sessionRef.current?.nodeID)
      setToolState('select')
  }, [activeNode, tool])

  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (pendingRef.current.size === 0) return
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', beforeUnload)
    return () => {
      window.removeEventListener('beforeunload', beforeUnload)
      window.clearTimeout(persistTimerRef.current)
      if (pendingRef.current.size > 0 && !blockedRef.current)
        void persistRef.current()
      sessionRef.current?.worker.close()
    }
  }, [])

  return {
    tool,
    activate,
    available,
    loading,
    saving,
    blocked,
    settings,
    setSettings,
    brush,
    resources,
    history,
    mutate,
    undo,
    redo,
    flush,
  }
}

function tileKey(tile: { tileX: number; tileY: number }) {
  return `${tile.tileX}:${tile.tileY}`
}

function isDefault(alpha: Uint8Array, defaultAlpha: number) {
  for (const value of alpha) if (value !== defaultAlpha) return false
  return true
}

function cloneTile(tile: RasterMaskTileSnapshot): RasterMaskTileSnapshot {
  return { ...tile, alpha: tile.alpha.slice() }
}

export function screenPointToRasterMask(
  clientX: number,
  clientY: number,
  bounds: DOMRect,
  view: { zoom: number; panX: number; panY: number },
  worldTransform: EditorTransform,
): RasterMaskPoint | undefined {
  const scale = view.zoom / 100
  const worldX = (clientX - bounds.left - bounds.width / 2 - view.panX) / scale
  const worldY = (clientY - bounds.top - bounds.height / 2 - view.panY) / scale
  const [a, b, c, d, tx, ty] = worldTransform
  const determinant = a * d - b * c
  if (!Number.isFinite(determinant) || Math.abs(determinant) < 1e-8)
    return undefined
  return {
    x: (d * (worldX - tx) - c * (worldY - ty)) / determinant,
    y: (-b * (worldX - tx) + a * (worldY - ty)) / determinant,
  }
}
