import { useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, PointerEvent as ReactPointerEvent } from 'react'

import type { Asset } from '#/lib/api'
import { screenPointToWorld, zoomAtScreenPoint } from '#/lib/editor-transform'
import type { EditorDocumentV2 } from './domain/document-v2'
import {
  editorSelectionContainsNode,
  editorSelectionBounds,
  hitTestEditorDocument,
  transformEditorNodesAroundWorldPoint,
  translateEditorNodes,
} from './domain/canvas-interaction-v2'
import type { EditorViewport } from './renderer/types'

type Props = {
  document: EditorDocumentV2
  assets: ReadonlyMap<string, Asset>
  view: EditorViewport
  selectedIDs: ReadonlySet<string>
  disabled?: boolean
  onViewChange: (view: EditorViewport) => void
  onSelectionChange: (ids: ReadonlySet<string>, activeID: string) => void
  onPreview: (document: EditorDocumentV2) => void
  onCommit: (
    initial: EditorDocumentV2,
    final: EditorDocumentV2,
    mergeKey?: string,
  ) => void
  onFit: () => void
}

export function StructuredCanvasInteraction({
  document,
  assets,
  view,
  selectedIDs,
  disabled,
  onViewChange,
  onSelectionChange,
  onPreview,
  onCommit,
  onFit,
}: Props) {
  const rootRef = useRef<HTMLDivElement>(null)
  const cancelPointerSessionRef = useRef<() => void>(() => undefined)
  const [spacePressed, setSpacePressed] = useState(false)
  const [dragging, setDragging] = useState(false)
  const selectionBounds = useMemo(
    () => editorSelectionBounds(document, assets, selectedIDs),
    [assets, document, selectedIDs],
  )
  const scale = view.zoom / 100

  useEffect(() => {
    if (!spacePressed) return
    const release = (event: KeyboardEvent) => {
      if (event.code === 'Space') setSpacePressed(false)
    }
    window.addEventListener('keyup', release)
    return () => window.removeEventListener('keyup', release)
  }, [spacePressed])

  useEffect(
    () => () => {
      cancelPointerSessionRef.current()
    },
    [],
  )

  function beginPointer(event: ReactPointerEvent<HTMLDivElement>) {
    const root = rootRef.current
    if (!root || event.button > 1) return
    root.focus({ preventScroll: true })
    const rect = root.getBoundingClientRect()
    const pan = spacePressed || event.button === 1
    event.preventDefault()
    if (pan) {
      const initialView = view
      const start = { x: event.clientX, y: event.clientY }
      setDragging(true)
      const move = (moveEvent: PointerEvent) =>
        onViewChange({
          ...initialView,
          panX: initialView.panX + moveEvent.clientX - start.x,
          panY: initialView.panY + moveEvent.clientY - start.y,
        })
      const stop = () => {
        finish(move, stop)
        cancelPointerSessionRef.current = () => undefined
      }
      cancelPointerSessionRef.current = () =>
        finish(move, stop, stop, { updateState: false })
      window.addEventListener('pointermove', move)
      window.addEventListener('pointerup', stop)
      window.addEventListener('pointercancel', stop)
      return
    }

    const point = screenPointToWorld(event.clientX, event.clientY, rect, view)
    const hitID = hitTestEditorDocument(document, assets, point)
    if (!hitID) {
      if (!event.shiftKey) onSelectionChange(new Set(), '')
      return
    }
    const hitInsideSelection = editorSelectionContainsNode(
      document,
      selectedIDs,
      hitID,
    )
    const nextSelection = event.shiftKey
      ? new Set(selectedIDs)
      : hitInsideSelection
        ? new Set(selectedIDs)
        : new Set([hitID])
    if (event.shiftKey) {
      if (nextSelection.has(hitID)) nextSelection.delete(hitID)
      else nextSelection.add(hitID)
    }
    const activeID = nextSelection.has(hitID)
      ? hitID
      : ([...nextSelection].at(-1) ?? '')
    onSelectionChange(nextSelection, activeID)
    if (
      disabled ||
      (!nextSelection.has(hitID) && !hitInsideSelection) ||
      nextSelection.size === 0
    )
      return

    const initialDocument = document
    const start = point
    let finalDocument = document
    let changed = false
    const move = (moveEvent: PointerEvent) => {
      const current = screenPointToWorld(
        moveEvent.clientX,
        moveEvent.clientY,
        rect,
        view,
      )
      const delta = { x: current.x - start.x, y: current.y - start.y }
      if (!changed && Math.hypot(delta.x, delta.y) * scale < 2) return
      try {
        finalDocument = translateEditorNodes(
          initialDocument,
          nextSelection,
          delta,
        )
      } catch {
        return
      }
      changed = true
      setDragging(true)
      onPreview(finalDocument)
    }
    const stop = (commit: boolean) => {
      finish(move, pointerUp, pointerCancel)
      cancelPointerSessionRef.current = () => undefined
      if (!commit && changed) onPreview(initialDocument)
      if (commit && changed) onCommit(initialDocument, finalDocument)
    }
    const pointerUp = () => stop(true)
    const pointerCancel = () => stop(false)
    cancelPointerSessionRef.current = () =>
      finish(move, pointerUp, pointerCancel, { updateState: false })
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', pointerUp)
    window.addEventListener('pointercancel', pointerCancel)
  }

  function beginSelectionTransform(
    event: ReactPointerEvent<HTMLButtonElement>,
    kind: 'scale' | 'rotate',
  ) {
    const root = rootRef.current
    if (!root || !selectionBounds || disabled || selectedIDs.size === 0) return
    event.preventDefault()
    event.stopPropagation()
    root.focus({ preventScroll: true })
    const initialDocument = document
    const center = {
      x: selectionBounds.centerX,
      y: selectionBounds.centerY,
    }
    const rect = root.getBoundingClientRect()
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
    let finalDocument = document
    let changed = false
    const move = (moveEvent: PointerEvent) => {
      try {
        if (kind === 'scale') {
          const distance = Math.hypot(
            moveEvent.clientX - screenCenter.x,
            moveEvent.clientY - screenCenter.y,
          )
          const factor = Math.min(50, Math.max(0.02, distance / startDistance))
          changed = Math.abs(factor - 1) > 1e-6
          finalDocument = transformEditorNodesAroundWorldPoint(
            initialDocument,
            selectedIDs,
            center,
            { type: 'scale', factor },
          )
        } else {
          const angle = Math.atan2(
            moveEvent.clientY - screenCenter.y,
            moveEvent.clientX - screenCenter.x,
          )
          let degrees = ((angle - startAngle) * 180) / Math.PI
          if (moveEvent.shiftKey) degrees = Math.round(degrees / 15) * 15
          changed = Math.abs(degrees) > 1e-6
          finalDocument = transformEditorNodesAroundWorldPoint(
            initialDocument,
            selectedIDs,
            center,
            { type: 'rotate', degrees },
          )
        }
      } catch {
        return
      }
      if (changed) onPreview(finalDocument)
    }
    const stop = (commit: boolean) => {
      finish(move, pointerUp, pointerCancel)
      cancelPointerSessionRef.current = () => undefined
      if (!commit && changed) onPreview(initialDocument)
      if (commit && changed) onCommit(initialDocument, finalDocument)
    }
    const pointerUp = () => stop(true)
    const pointerCancel = () => stop(false)
    cancelPointerSessionRef.current = () =>
      finish(move, pointerUp, pointerCancel, { updateState: false })
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', pointerUp)
    window.addEventListener('pointercancel', pointerCancel)
  }

  function commitSelectionTransform(
    operation:
      { type: 'scale'; factor: number } | { type: 'rotate'; degrees: number },
  ) {
    if (!selectionBounds || disabled || selectedIDs.size === 0) return
    try {
      const next = transformEditorNodesAroundWorldPoint(
        document,
        selectedIDs,
        { x: selectionBounds.centerX, y: selectionBounds.centerY },
        operation,
      )
      onCommit(document, next)
    } catch {
      // Protocol limits and locked ancestors intentionally reject transforms.
    }
  }

  function finish(
    move: (event: PointerEvent) => void,
    pointerUp: () => void,
    pointerCancel = pointerUp,
    options: { updateState?: boolean } = {},
  ) {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', pointerUp)
    window.removeEventListener('pointercancel', pointerCancel)
    if (options.updateState !== false) setDragging(false)
  }

  return (
    <div
      ref={rootRef}
      className="structured-canvas-interaction"
      data-panning={spacePressed || dragging || undefined}
      role="region"
      aria-label="专业图层画布"
      tabIndex={0}
      onPointerDown={beginPointer}
      onWheel={(event) => {
        event.preventDefault()
        if (!(event.ctrlKey || event.metaKey)) {
          onViewChange({
            ...view,
            panX: view.panX - (event.shiftKey ? event.deltaY : event.deltaX),
            panY: view.panY - (event.shiftKey ? 0 : event.deltaY),
          })
          return
        }
        const rect = event.currentTarget.getBoundingClientRect()
        onViewChange(
          zoomAtScreenPoint(
            view,
            Math.min(
              400,
              Math.max(10, view.zoom * Math.exp(-event.deltaY * 0.002)),
            ),
            event.clientX,
            event.clientY,
            rect,
          ),
        )
      }}
      onKeyDown={(event) => {
        if (event.code === 'Space') {
          event.preventDefault()
          setSpacePressed(true)
          return
        }
        if (event.key === 'Escape') {
          onSelectionChange(new Set(), '')
          return
        }
        if (event.key === '0') {
          event.preventDefault()
          onFit()
          return
        }
        if (event.key === '1') {
          event.preventDefault()
          const rect = event.currentTarget.getBoundingClientRect()
          onViewChange(
            zoomAtScreenPoint(
              view,
              100,
              rect.left + rect.width / 2,
              rect.top + rect.height / 2,
              rect,
            ),
          )
          return
        }
        if (
          disabled ||
          !['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(
            event.key,
          ) ||
          selectedIDs.size === 0
        )
          return
        event.preventDefault()
        const distance = event.shiftKey ? 10 : 1
        const delta = {
          x:
            event.key === 'ArrowLeft'
              ? -distance
              : event.key === 'ArrowRight'
                ? distance
                : 0,
          y:
            event.key === 'ArrowUp'
              ? -distance
              : event.key === 'ArrowDown'
                ? distance
                : 0,
        }
        try {
          const next = translateEditorNodes(document, selectedIDs, delta)
          onCommit(document, next, `nudge:${[...selectedIDs].sort().join(',')}`)
        } catch {
          // Locked selections remain selectable but cannot be transformed.
        }
      }}
    >
      <div
        className="structured-artboard-outline"
        style={
          {
            width: document.canvas.width,
            height: document.canvas.height,
            transform: `translate(${view.panX}px, ${view.panY}px) scale(${scale})`,
            '--editor-zoom': scale,
          } as CSSProperties
        }
        aria-hidden="true"
      />
      {selectionBounds && (
        <div
          className="structured-selection-world"
          style={{
            transform: `translate(${view.panX}px, ${view.panY}px) scale(${scale})`,
          }}
        >
          <div
            className="structured-selection-outline"
            style={
              {
                left: selectionBounds.left,
                top: selectionBounds.top,
                width: selectionBounds.width,
                height: selectionBounds.height,
                '--editor-zoom': scale,
              } as CSSProperties
            }
          >
            {(['nw', 'ne', 'se', 'sw'] as const).map((handle) => (
              <button
                key={handle}
                type="button"
                className={`structured-transform-handle is-${handle}`}
                aria-label="等比缩放所选图层"
                aria-hidden={handle !== 'se'}
                tabIndex={handle === 'se' ? 0 : -1}
                title={handle === 'se' ? '等比缩放（方向键微调）' : undefined}
                onPointerDown={(event) =>
                  beginSelectionTransform(event, 'scale')
                }
                onKeyDown={(event) => {
                  if (
                    ![
                      'ArrowUp',
                      'ArrowDown',
                      'ArrowLeft',
                      'ArrowRight',
                    ].includes(event.key)
                  )
                    return
                  event.preventDefault()
                  event.stopPropagation()
                  commitSelectionTransform({
                    type: 'scale',
                    factor:
                      event.key === 'ArrowUp' || event.key === 'ArrowRight'
                        ? 1.05
                        : 0.95,
                  })
                }}
              />
            ))}
            <span className="structured-rotate-stem" />
            <button
              type="button"
              className="structured-transform-handle is-rotate"
              aria-label="旋转所选图层"
              title="旋转（左右方向键微调）"
              onPointerDown={(event) =>
                beginSelectionTransform(event, 'rotate')
              }
              onKeyDown={(event) => {
                if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight')
                  return
                event.preventDefault()
                event.stopPropagation()
                commitSelectionTransform({
                  type: 'rotate',
                  degrees: event.key === 'ArrowLeft' ? -15 : 15,
                })
              }}
            />
          </div>
        </div>
      )}
    </div>
  )
}
