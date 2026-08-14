import { useEffect, useMemo, useRef, useState } from 'react'
import type { PointerEvent as ReactPointerEvent, RefObject } from 'react'

import type { Asset } from '#/lib/api'
import { editorNodeWorldTransform } from '../../domain/canvas-interaction-v2'
import type { EditorDocumentV2, EditorNodeV2 } from '../../domain/document-v2'
import type { EditorViewport } from '../../renderer/types'
import type { RasterMaskBrush } from './tile-mask'
import type { RasterMaskTool } from './use-raster-mask-editor'
import { screenPointToRasterMask } from './use-raster-mask-editor'
import type { RasterMaskWorkerClient } from './worker-client'

type Mutation = {
  tiles: Array<{
    tileX: number
    tileY: number
    width: number
    height: number
    alpha: Uint8Array
  }>
  canUndo: boolean
  canRedo: boolean
}

type Props = {
  viewportRef: RefObject<HTMLDivElement | null>
  document: EditorDocumentV2
  node: EditorNodeV2
  asset: Asset
  view: EditorViewport
  tool: Exclude<RasterMaskTool, 'select'>
  brush: RasterMaskBrush
  disabled?: boolean
  mutate: (
    action: (worker: RasterMaskWorkerClient) => Promise<Mutation>,
    persistAfter?: boolean,
  ) => Promise<Mutation>
  onNotice: (message: string) => void
}

export function RasterMaskOverlay({
  viewportRef,
  document,
  node,
  asset,
  view,
  tool,
  brush,
  disabled,
  mutate,
  onNotice,
}: Props) {
  const rootRef = useRef<HTMLDivElement>(null)
  const [cursor, setCursor] = useState<{ x: number; y: number }>()
  const queueRef = useRef(Promise.resolve())
  const strokeRef = useRef<string | undefined>(undefined)
  const nodes = useMemo(
    () => new Map(document.nodes.map((candidate) => [candidate.id, candidate])),
    [document.nodes],
  )
  const worldTransform = useMemo(
    () => editorNodeWorldTransform(nodes, node.id),
    [node.id, nodes],
  )
  const visualScale =
    (Math.hypot(worldTransform[0], worldTransform[1]) +
      Math.hypot(worldTransform[2], worldTransform[3])) /
    2
  const cursorSize = Math.max(4, brush.size * visualScale * (view.zoom / 100))

  useEffect(
    () => () => {
      strokeRef.current = undefined
    },
    [],
  )

  function point(event: PointerEvent | ReactPointerEvent) {
    const viewport = viewportRef.current
    if (!viewport) return undefined
    const value = screenPointToRasterMask(
      event.clientX,
      event.clientY,
      viewport.getBoundingClientRect(),
      view,
      worldTransform,
    )
    if (
      !value ||
      value.x < 0 ||
      value.y < 0 ||
      value.x >= asset.width ||
      value.y >= asset.height
    )
      return undefined
    return {
      ...value,
      pressure:
        'pressure' in event && event.pressure > 0 ? event.pressure : 0.5,
    }
  }

  function enqueue(task: () => Promise<unknown>) {
    queueRef.current = queueRef.current
      .then(task)
      .then(() => undefined)
      .catch((error: unknown) => {
        onNotice(
          error instanceof Error ? error.message : '蒙版笔刷暂时无法使用',
        )
      })
    return queueRef.current
  }

  function begin(event: ReactPointerEvent<HTMLDivElement>) {
    if (disabled || event.button !== 0) return
    const initial = point(event)
    if (!initial) return
    event.preventDefault()
    event.currentTarget.setPointerCapture(event.pointerId)
    const strokeID = crypto.randomUUID()
    strokeRef.current = strokeID
    enqueue(() =>
      mutate(
        (worker) =>
          worker.beginStroke(
            strokeID,
            { ...brush, mode: tool === 'eraser' ? 'erase' : 'paint' },
            initial,
          ),
        false,
      ),
    )
  }

  function move(event: ReactPointerEvent<HTMLDivElement>) {
    setCursor({ x: event.nativeEvent.offsetX, y: event.nativeEvent.offsetY })
    const strokeID = strokeRef.current
    if (!strokeID) return
    const events = event.nativeEvent.getCoalescedEvents()
    const points = events
      .map(point)
      .filter(
        (value): value is NonNullable<typeof value> => value !== undefined,
      )
    if (!points.length) return
    enqueue(() => mutate((worker) => worker.addPoints(strokeID, points), false))
  }

  function finish(commit: boolean) {
    const strokeID = strokeRef.current
    if (!strokeID) return
    strokeRef.current = undefined
    enqueue(() =>
      mutate(
        (worker) =>
          commit
            ? worker.commitStroke(strokeID)
            : worker.cancelStroke(strokeID),
        commit,
      ),
    )
  }

  return (
    <div
      ref={rootRef}
      className="raster-mask-overlay"
      data-tool={tool}
      aria-label={tool === 'brush' ? '蒙版画笔画布' : '蒙版橡皮擦画布'}
      role="application"
      tabIndex={0}
      onPointerDown={begin}
      onPointerMove={move}
      onPointerUp={() => finish(true)}
      onPointerCancel={() => finish(false)}
      onPointerLeave={() => {
        if (!strokeRef.current) setCursor(undefined)
      }}
    >
      {cursor && (
        <span
          className="raster-brush-cursor"
          aria-hidden="true"
          style={{
            width: cursorSize,
            height: cursorSize,
            transform: `translate3d(${cursor.x - cursorSize / 2}px, ${cursor.y - cursorSize / 2}px, 0)`,
          }}
        />
      )}
    </div>
  )
}
