// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Asset } from '#/lib/api'
import type { EditorDocumentV2 } from './domain/document-v2'
import { StructuredCanvasInteraction } from './structured-canvas-interaction'

const asset: Asset = {
  id: '00000000-0000-4000-8000-000000000001',
  kind: 'upload',
  media_type: 'image/png',
  width: 100,
  height: 80,
  byte_size: 100,
  sha256: 'asset',
  url: '/asset',
  thumb_320_url: '/asset-320',
  thumb_640_url: '/asset-640',
  thumb_1280_url: '/asset-1280',
  created_at: '2026-08-14T00:00:00Z',
}

afterEach(cleanup)

describe('StructuredCanvasInteraction', () => {
  it('selects and commits a pointer drag exactly once', () => {
    const onSelectionChange = vi.fn()
    const onPreview = vi.fn()
    const onCommit = vi.fn()
    render(
      <StructuredCanvasInteraction
        document={document()}
        assets={new Map([[asset.id, asset]])}
        view={{ zoom: 100, panX: -50, panY: -40 }}
        selectedIDs={new Set()}
        onViewChange={vi.fn()}
        onSelectionChange={onSelectionChange}
        onPreview={onPreview}
        onCommit={onCommit}
        onFit={vi.fn()}
      />,
    )
    const canvas = screen.getByRole('region', { name: '专业图层画布' })
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue(rect())

    fireEvent.pointerDown(canvas, { button: 0, clientX: 60, clientY: 70 })
    fireEvent.pointerMove(window, { clientX: 80, clientY: 75 })
    fireEvent.pointerMove(window, { clientX: 90, clientY: 80 })
    fireEvent.pointerUp(window)

    expect(onSelectionChange).toHaveBeenCalledWith(new Set(['layer']), 'layer')
    expect(onPreview).toHaveBeenCalledTimes(2)
    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(
      onCommit.mock.calls[0][1].nodes.find(
        (node: { id: string }) => node.id === 'layer',
      ).transform,
    ).toEqual([1, 0, 0, 1, 30, 10])
  })

  it('pans with Space and nudges the current selection', () => {
    const onViewChange = vi.fn()
    const onCommit = vi.fn()
    render(
      <StructuredCanvasInteraction
        document={document()}
        assets={new Map([[asset.id, asset]])}
        view={{ zoom: 100, panX: -50, panY: -40 }}
        selectedIDs={new Set(['layer'])}
        onViewChange={onViewChange}
        onSelectionChange={vi.fn()}
        onPreview={vi.fn()}
        onCommit={onCommit}
        onFit={vi.fn()}
      />,
    )
    const canvas = screen.getByRole('region', { name: '专业图层画布' })
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue(rect())

    fireEvent.keyDown(canvas, { code: 'Space' })
    fireEvent.pointerDown(canvas, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(window, { clientX: 112, clientY: 107 })
    fireEvent.pointerUp(window)
    expect(onViewChange).toHaveBeenCalledWith({
      zoom: 100,
      panX: -38,
      panY: -33,
    })

    fireEvent.keyDown(canvas, { key: 'ArrowRight', shiftKey: true })
    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(onCommit.mock.calls[0][2]).toBe('nudge:layer')
    expect(onCommit.mock.calls[0][1].nodes[0].transform).toEqual([
      1, 0, 0, 1, 10, 0,
    ])
  })

  it('restores the initial document when a pointer drag is cancelled', () => {
    const initial = document()
    const onPreview = vi.fn()
    const onCommit = vi.fn()
    render(
      <StructuredCanvasInteraction
        document={initial}
        assets={new Map([[asset.id, asset]])}
        view={{ zoom: 100, panX: -50, panY: -40 }}
        selectedIDs={new Set(['layer'])}
        onViewChange={vi.fn()}
        onSelectionChange={vi.fn()}
        onPreview={onPreview}
        onCommit={onCommit}
        onFit={vi.fn()}
      />,
    )
    const canvas = screen.getByRole('region', { name: '专业图层画布' })
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue(rect())
    fireEvent.pointerDown(canvas, { button: 0, clientX: 60, clientY: 70 })
    fireEvent.pointerMove(window, { clientX: 80, clientY: 75 })
    fireEvent.pointerCancel(window)
    expect(onPreview).toHaveBeenLastCalledWith(initial)
    expect(onCommit).not.toHaveBeenCalled()
  })

  it('scales and rotates through constant-size selection handles', () => {
    const onPreview = vi.fn()
    const onCommit = vi.fn()
    render(
      <StructuredCanvasInteraction
        document={document()}
        assets={new Map([[asset.id, asset]])}
        view={{ zoom: 100, panX: -50, panY: -40 }}
        selectedIDs={new Set(['layer'])}
        onViewChange={vi.fn()}
        onSelectionChange={vi.fn()}
        onPreview={onPreview}
        onCommit={onCommit}
        onFit={vi.fn()}
      />,
    )
    const canvas = screen.getByRole('region', { name: '专业图层画布' })
    vi.spyOn(canvas, 'getBoundingClientRect').mockReturnValue(rect())

    fireEvent.pointerDown(
      screen.getByRole('button', { name: '等比缩放所选图层' }),
      { button: 0, clientX: 150, clientY: 140 },
    )
    fireEvent.pointerMove(window, { clientX: 200, clientY: 180 })
    fireEvent.pointerUp(window)
    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(onCommit.mock.calls[0][1].nodes[0].transform).toEqual([
      2, 0, 0, 2, -50, -40,
    ])

    fireEvent.pointerDown(
      screen.getByRole('button', { name: '旋转所选图层' }),
      { button: 0, clientX: 100, clientY: 50 },
    )
    fireEvent.pointerMove(window, {
      clientX: 150,
      clientY: 100,
      shiftKey: true,
    })
    fireEvent.pointerUp(window)
    expect(onCommit).toHaveBeenCalledTimes(2)
    expect(onCommit.mock.calls[1][1].nodes[0].transform.map(round)).toEqual([
      0, 1, -1, 0, 90, -10,
    ])

    fireEvent.keyDown(screen.getByRole('button', { name: '旋转所选图层' }), {
      key: 'ArrowLeft',
    })
    expect(onCommit).toHaveBeenCalledTimes(3)
  })
})

function document(): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: 100, height: 80 },
    nodes: [
      {
        id: 'layer',
        type: 'raster',
        parent_id: null,
        order_key: '00000000',
        transform: [1, 0, 0, 1, 0, 0],
        opacity: 1,
        blend_mode: 'normal',
        visible: true,
        locked: false,
        asset_id: asset.id,
      },
    ],
  }
}

function rect(): DOMRect {
  return {
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 200,
    bottom: 200,
    width: 200,
    height: 200,
    toJSON: () => ({}),
  }
}

function round(value: number) {
  return Math.round(value * 1e9) / 1e9
}
