import { useEffect, useMemo, useRef, useState } from 'react'

import type { EditorRenderDocument } from './scene-compiler'
import type { Asset } from '#/lib/api'
import type {
  EditorRasterMaskRenderResource,
  EditorRenderAsset,
  EditorRenderer,
  EditorViewport,
} from './types'

type PixiSurfaceProps = {
  enabled: boolean
  document: EditorRenderDocument
  assets?: ReadonlyMap<string, Asset>
  rasterMasks?: ReadonlyMap<string, EditorRasterMaskRenderResource>
  viewport: EditorViewport
  onUnavailable: (reason: string) => void
  onPresentedChange: (presented: boolean) => void
  retryKey?: number
}

export function PixiSurface({
  enabled,
  document,
  assets,
  rasterMasks,
  viewport,
  onUnavailable,
  onPresentedChange,
  retryKey = 0,
}: PixiSurfaceProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const rendererRef = useRef<EditorRenderer | undefined>(undefined)
  const onUnavailableRef = useRef(onUnavailable)
  const onPresentedChangeRef = useRef(onPresentedChange)
  const viewportRef = useRef(viewport)
  const [ready, setReady] = useState(false)
  const renderAssets = useMemo(() => toEditorRenderAssets(assets), [assets])

  useEffect(() => {
    onUnavailableRef.current = onUnavailable
  }, [onUnavailable])

  useEffect(() => {
    onPresentedChangeRef.current = onPresentedChange
  }, [onPresentedChange])

  useEffect(() => {
    viewportRef.current = viewport
  }, [viewport])

  useEffect(() => {
    if (!enabled || !canvasRef.current) return
    onPresentedChangeRef.current(false)
    const canvas = canvasRef.current
    const parent = canvas.parentElement
    if (!parent) return
    let disposed = false
    let failed = false
    const isDisposed = () => disposed || failed
    let renderer: EditorRenderer | undefined
    let initializationTimeout = 0
    let contextTimeout = 0
    const fail = (error: unknown) => {
      if (disposed || failed) return
      failed = true
      window.clearTimeout(initializationTimeout)
      window.clearTimeout(contextTimeout)
      renderer?.destroy()
      rendererRef.current = undefined
      setReady(false)
      onPresentedChangeRef.current(false)
      console.error('[editor-renderer] unavailable', error)
      onUnavailableRef.current(
        error instanceof Error ? error.message : 'WebGL 渲染器不可用',
      )
    }
    initializationTimeout = window.setTimeout(
      () => fail(new Error('画布初始化超时，请重新加载')),
      12_000,
    )
    void import('./pixi-renderer')
      .then(async ({ PixiEditorRenderer }) => {
        if (isDisposed()) return
        renderer = new PixiEditorRenderer()
        const rect = parent.getBoundingClientRect()
        await renderer.init(canvas, {
          width: Math.max(1, Math.round(rect.width)),
          height: Math.max(1, Math.round(rect.height)),
          resolution: Math.min(window.devicePixelRatio || 1, 2),
          onContextChange: (lost) => {
            window.clearTimeout(contextTimeout)
            if (lost) {
              onPresentedChangeRef.current(false)
              contextTimeout = window.setTimeout(
                () => fail(new Error('图形上下文恢复超时，请重新加载画布')),
                12_000,
              )
            } else onPresentedChangeRef.current(true)
          },
          onError: fail,
        })
        if (isDisposed()) {
          renderer.destroy()
          return
        }
        window.clearTimeout(initializationTimeout)
        rendererRef.current = renderer
        setReady(true)
      })
      .catch(fail)
    return () => {
      disposed = true
      window.clearTimeout(initializationTimeout)
      window.clearTimeout(contextTimeout)
      onPresentedChangeRef.current(false)
      setReady(false)
      rendererRef.current = undefined
      renderer?.destroy()
    }
  }, [enabled, retryKey])

  useEffect(() => {
    if (!enabled || !ready || !canvasRef.current) return
    const canvas = canvasRef.current
    const parent = canvas.parentElement
    const renderer = rendererRef.current
    if (!parent || !renderer) return
    const update = () => {
      const rect = parent.getBoundingClientRect()
      renderer.resize(
        Math.max(1, Math.round(rect.width)),
        Math.max(1, Math.round(rect.height)),
      )
      renderer.setViewport({
        zoom: viewportRef.current.zoom,
        panX: rect.width / 2 + viewportRef.current.panX,
        panY: rect.height / 2 + viewportRef.current.panY,
      })
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(parent)
    return () => observer.disconnect()
  }, [enabled, ready])

  useEffect(() => {
    const renderer = rendererRef.current
    const parent = canvasRef.current?.parentElement
    if (!enabled || !ready || !renderer || !parent) return
    const rect = parent.getBoundingClientRect()
    renderer.setViewport({
      zoom: viewport.zoom,
      panX: rect.width / 2 + viewport.panX,
      panY: rect.height / 2 + viewport.panY,
    })
    renderer.render()
  }, [enabled, ready, viewport.panX, viewport.panY, viewport.zoom])

  useEffect(() => {
    const renderer = rendererRef.current
    if (!enabled || !ready || !renderer) return
    void renderer
      .sync(document, renderAssets, rasterMasks)
      .then(() => onPresentedChangeRef.current(true))
      .catch((error: unknown) => {
        renderer.destroy()
        rendererRef.current = undefined
        setReady(false)
        onPresentedChangeRef.current(false)
        onUnavailableRef.current(
          error instanceof Error ? error.message : '画布资源加载失败',
        )
      })
  }, [document, enabled, rasterMasks, ready, renderAssets])

  if (!enabled) return null
  return (
    <canvas
      ref={canvasRef}
      className="editor-pixi-surface"
      data-testid="editor-pixi-surface"
      aria-hidden="true"
    />
  )
}

export function toEditorRenderAssets(assets?: ReadonlyMap<string, Asset>) {
  const result = new Map<string, EditorRenderAsset>()
  if (!assets) return result
  for (const asset of assets.values()) {
    const variants = [
      variant(asset.thumb_320_url, asset, 320),
      variant(asset.thumb_640_url, asset, 640),
      variant(asset.thumb_1280_url, asset, 1280),
      { url: asset.url, width: asset.width, height: asset.height },
    ].filter(
      (item, index, all) =>
        item.url &&
        all.findIndex((candidate) => candidate.url === item.url) === index,
    )
    result.set(asset.id, {
      id: asset.id,
      width: asset.width,
      height: asset.height,
      variants,
    })
  }
  return result
}

function variant(url: string, asset: Asset, maximumEdge: number) {
  const scale = Math.min(1, maximumEdge / Math.max(asset.width, asset.height))
  return {
    url,
    width: Math.max(1, Math.round(asset.width * scale)),
    height: Math.max(1, Math.round(asset.height * scale)),
  }
}
