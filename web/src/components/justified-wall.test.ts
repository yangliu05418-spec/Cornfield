import { describe, expect, it } from 'vitest'

import { buildWallItems } from './justified-wall'
import type { Asset, GenerationBatch } from '#/lib/api'

describe('buildWallItems', () => {
  it('creates one placeholder per expected draw output before assets', () => {
    const batch = {
      id: 'batch',
      model_id: 'midjourney-v7',
      prompt: 'test',
      aspect_ratio: '16:9',
      resolution: 'standard',
      draw_count: 1,
      expected_outputs: 4,
      completed_outputs: 0,
      status: 'running',
      created_at: '',
      jobs: [
        {
          id: 'job',
          draw_index: 0,
          status: 'provider_pending',
          expected_outputs: 4,
        },
      ],
    } satisfies GenerationBatch
    const asset = { id: 'asset', width: 640, height: 480 } as Asset
    const items = buildWallItems([asset], [batch])
    expect(items).toHaveLength(5)
    expect(items.slice(0, 4).every((item) => item.jobID === 'job')).toBe(true)
    expect(items[4].asset?.id).toBe('asset')
  })

  it('keeps a terminal draw visible without a cancel action', () => {
    const batch = {
      id: 'batch',
      prompt: 'test',
      aspect_ratio: '1:1',
      status: 'cancelled',
      jobs: [
        {
          id: 'job',
          status: 'cancelled',
          expected_outputs: 1,
          draw_index: 0,
        },
      ],
    } as unknown as GenerationBatch
    const items = buildWallItems([], [batch])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ status: 'cancelled', cancellable: false })
  })

  it('treats uncertain submissions as terminal and non-cancellable', () => {
    const batch = {
      id: 'batch',
      prompt: 'test',
      aspect_ratio: '1:1',
      status: 'submission_uncertain',
      jobs: [
        {
          id: 'job',
          status: 'submission_uncertain',
          expected_outputs: 1,
          draw_index: 0,
        },
      ],
    } as unknown as GenerationBatch

    const items = buildWallItems([], [batch])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({
      status: 'submission_uncertain',
      cancellable: false,
    })
  })

  it('reuses the output slot when the API supplies generation linkage', () => {
    const batch = {
      id: 'batch',
      prompt: 'test',
      aspect_ratio: '1:1',
      status: 'running',
      jobs: [
        {
          id: 'job',
          status: 'provider_pending',
          expected_outputs: 1,
          draw_index: 0,
        },
      ],
    } as unknown as GenerationBatch
    const asset = {
      id: 'asset',
      width: 1024,
      height: 1024,
      job_id: 'job',
      output_index: 0,
    } as Asset

    const items = buildWallItems([asset], [batch])

    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ id: 'job:0', asset })
  })

  it('does not render a deleted output as a completed blank slot', () => {
    const batch = {
      id: 'batch',
      prompt: 'test',
      aspect_ratio: '1:1',
      status: 'running',
      jobs: [
        {
          id: 'job',
          status: 'provider_pending',
          expected_outputs: 4,
          deleted_outputs: [2],
          draw_index: 0,
        },
      ],
    } as unknown as GenerationBatch

    const items = buildWallItems([], [batch])
    expect(items).toHaveLength(3)
    expect(items.map((item) => item.outputIndex)).toEqual([0, 1, 3])
  })

  it('removes every placeholder belonging to a dismissed draw', () => {
    const batch = {
      id: 'batch',
      prompt: 'test',
      aspect_ratio: '1:1',
      status: 'failed',
      jobs: [
        {
          id: 'job',
          status: 'failed',
          expected_outputs: 4,
          dismissed_at: '2026-07-21T00:00:00Z',
          draw_index: 0,
        },
      ],
    } as unknown as GenerationBatch

    expect(buildWallItems([], [batch])).toEqual([])
  })
})
