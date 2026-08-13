import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef, useState } from 'react'

import { api } from '#/lib/api'
import type {
  Asset,
  AssetOperation,
  EditorProject,
  LayerSet,
  Model,
} from '#/lib/api'
import { mergeAssetIntoCaches } from '#/lib/asset-cache'

const terminalStates = new Set([
  'succeeded',
  'failed',
  'cancelled',
  'submission_uncertain',
])

export type LayerDecompositionSettings = {
  prompt: string
  resolution: 'auto' | '1K' | '1.5K' | '2K'
  mode: 'standard' | 'fast'
}

type UseEditorOperationsOptions = {
  projectID: string
  initialOperationID?: string
  activeLayerSet?: LayerSet
  getRevision: () => number
  flushSaves: () => Promise<void>
  onLayerSetReady: (layerSet: LayerSet, sourceRevision: number) => void
  onNotice: (message: string) => void
}

export function useEditorOperations(options: UseEditorOperationsOptions) {
  const queryClient = useQueryClient()
  const callbacksRef = useRef(options)
  callbacksRef.current = options
  const [operationID, setOperationID] = useState(options.initialOperationID)
  const [packageOperationID, setPackageOperationID] = useState<string>()
  const [lastLayerSet, setLastLayerSet] = useState(options.activeLayerSet)
  const [elapsed, setElapsed] = useState(0)
  const handledLayerSetRef = useRef('')
  const handledPublishRef = useRef('')

  useEffect(() => {
    if (!operationID && options.initialOperationID)
      setOperationID(options.initialOperationID)
  }, [operationID, options.initialOperationID])

  useEffect(() => {
    if (options.activeLayerSet) setLastLayerSet(options.activeLayerSet)
  }, [options.activeLayerSet])

  const modelsQuery = useQuery({
    queryKey: ['models'],
    queryFn: () => api<{ revision: string; models: Model[] }>('/api/v1/models'),
    staleTime: 30_000,
  })
  const operationQuery = useQuery({
    queryKey: ['asset-operation', operationID],
    enabled: Boolean(operationID),
    queryFn: () =>
      api<AssetOperation>(`/api/v1/asset-operations/${operationID}`),
    refetchInterval: (query) =>
      terminalStates.has(query.state.data?.status ?? '') ? false : 2_000,
  })
  const packageOperationQuery = useQuery({
    queryKey: ['asset-operation', packageOperationID],
    enabled: Boolean(packageOperationID),
    queryFn: () =>
      api<AssetOperation>(`/api/v1/asset-operations/${packageOperationID}`),
    refetchInterval: (query) =>
      terminalStates.has(query.state.data?.status ?? '') ? false : 2_000,
  })
  const operation = operationQuery.data
  const currentLayerSet = operation?.layer_set ?? lastLayerSet
  const running = Boolean(
    operationID && !terminalStates.has(operation?.status ?? ''),
  )
  const capability = modelsQuery.data?.models.find(
    (model) => model.id === 'byteplus-seedream-5-0-pro',
  )
  const canDecompose = Boolean(
    capability?.availability.can_submit &&
    capability.capabilities.layer_decomposition,
  )

  useEffect(() => {
    if (!running) {
      setElapsed(0)
      return
    }
    const parsed = Date.parse(
      operation?.started_at ?? operation?.created_at ?? '',
    )
    const startedAt = Number.isFinite(parsed) ? parsed : Date.now()
    const update = () =>
      setElapsed(Math.max(0, Math.floor((Date.now() - startedAt) / 1_000)))
    update()
    const timer = window.setInterval(update, 1_000)
    return () => window.clearInterval(timer)
  }, [operation?.created_at, operation?.started_at, running])

  useEffect(() => {
    if (!operationID) return
    const source = new EventSource('/api/v1/events')
    let reconciliation: number | undefined
    const onOperationEvent = (event: MessageEvent) => {
      try {
        const envelope = JSON.parse(event.data) as {
          payload?: Partial<AssetOperation> & { id?: string }
        }
        const payload = envelope.payload ?? {}
        if (payload.id !== operationID) return
        queryClient.setQueryData<AssetOperation>(
          ['asset-operation', operationID],
          (current) => (current ? { ...current, ...payload } : current),
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

  useEffect(() => {
    const layerSet = operation?.layer_set
    if (!operation || operation.status !== 'succeeded' || !layerSet) return
    setLastLayerSet(layerSet)
    if (layerSet.applied_to_project)
      queryClient.setQueryData<EditorProject>(
        ['editor-project', options.projectID],
        (current) =>
          current
            ? {
                ...current,
                active_layer_set_id: layerSet.id,
                active_layer_set: layerSet,
              }
            : current,
      )
    const key = `${operation.id}:${layerSet.id}`
    if (handledLayerSetRef.current === key) return
    handledLayerSetRef.current = key
    callbacksRef.current.onLayerSetReady(layerSet, operation.source_revision)
  }, [operation, options.projectID, queryClient])

  useEffect(() => {
    if (operation?.status !== 'failed') return
    callbacksRef.current.onNotice(
      operation.error_message ?? '智能分层失败，请稍后重试',
    )
  }, [operation?.id, operation?.status, operation?.error_message])

  useEffect(() => {
    if (
      operation?.operation_type !== 'editor_publish' ||
      operation.status !== 'succeeded' ||
      !operation.result_asset_id ||
      handledPublishRef.current === operation.id
    )
      return
    handledPublishRef.current = operation.id
    void api<Asset>(`/api/v1/assets/${operation.result_asset_id}`).then(
      (asset) => {
        mergeAssetIntoCaches(queryClient, asset)
        callbacksRef.current.onNotice('新图片已置入灵感墙顶部')
      },
      () => callbacksRef.current.onNotice('图片已生成，资产列表正在对账'),
    )
  }, [operation, queryClient])

  useEffect(() => {
    const result = packageOperationQuery.data
    const layerSet = currentLayerSet
    if (!result || !layerSet) return
    if (result.status === 'succeeded') {
      setPackageOperationID(undefined)
      window.location.assign(
        `/api/v1/layer-sets/${layerSet.id}/package/content`,
      )
    } else if (result.status === 'failed') {
      callbacksRef.current.onNotice(
        result.error_message ?? '创建图层压缩包失败',
      )
      setPackageOperationID(undefined)
    }
  }, [packageOperationQuery.data, currentLayerSet])

  const startDecomposition = useCallback(
    async (settings: LayerDecompositionSettings) => {
      if (!canDecompose) {
        callbacksRef.current.onNotice('智能分层暂未开放')
        return false
      }
      try {
        await callbacksRef.current.flushSaves()
        const result = await api<{ id: string }>(
          `/api/v1/editor-projects/${options.projectID}/layer-decompositions`,
          {
            method: 'POST',
            headers: { 'Idempotency-Key': crypto.randomUUID() },
            body: JSON.stringify({
              expected_revision: callbacksRef.current.getRevision(),
              prompt: settings.prompt,
              resolution: settings.resolution,
              prompt_optimization_mode: settings.mode,
            }),
          },
        )
        setOperationID(result.id)
        return true
      } catch (error) {
        callbacksRef.current.onNotice(
          error instanceof Error ? error.message : '无法启动智能分层',
        )
        return false
      }
    },
    [canDecompose, options.projectID],
  )

  const publish = useCallback(async () => {
    try {
      await callbacksRef.current.flushSaves()
      const result = await api<{ id: string }>(
        `/api/v1/editor-projects/${options.projectID}/publish`,
        {
          method: 'POST',
          headers: { 'Idempotency-Key': crypto.randomUUID() },
          body: JSON.stringify({
            expected_revision: callbacksRef.current.getRevision(),
          }),
        },
      )
      setOperationID(result.id)
      callbacksRef.current.onNotice('正在保存为新图片')
    } catch (error) {
      callbacksRef.current.onNotice(
        error instanceof Error ? error.message : '保存新图片失败',
      )
    }
  }, [options.projectID])

  const publishLayer = useCallback(
    async (layerSetID: string, itemID: string) => {
      try {
        const asset = await api<Asset>(
          `/api/v1/layer-sets/${layerSetID}/items/${itemID}/publish`,
          { method: 'POST' },
        )
        mergeAssetIntoCaches(queryClient, asset)
        callbacksRef.current.onNotice('图层已保存为新图片')
      } catch (error) {
        callbacksRef.current.onNotice(
          error instanceof Error ? error.message : '保存图层失败',
        )
      }
    },
    [queryClient],
  )

  const packageLayers = useCallback(async () => {
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
      callbacksRef.current.onNotice('正在后台整理图层压缩包')
    } catch (error) {
      callbacksRef.current.onNotice(
        error instanceof Error ? error.message : '创建压缩包失败',
      )
    }
  }, [currentLayerSet])

  return {
    operation,
    operationID,
    running,
    elapsed,
    currentLayerSet,
    canDecompose,
    capabilityMessage: capability?.availability.message,
    packageRunning: Boolean(packageOperationID),
    startDecomposition,
    publish,
    publishLayer,
    packageLayers,
  }
}
