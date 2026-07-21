import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'

import { applyGenerationEvent, failedJobAction } from './app.create'
import type { Asset, GenerationBatch } from '#/lib/api'

describe('generation SSE cache', () => {
  it('merges successful assets idempotently and replaces the job state', () => {
    const client = new QueryClient()
    const batch = {
      id: 'batch',
      status: 'running',
      jobs: [{ id: 'job', status: 'ingesting', expected_outputs: 1 }],
    } as unknown as GenerationBatch
    client.setQueryData(['generations'], {
      pages: [{ items: [batch], next_cursor: '' }],
      pageParams: [''],
    })
    client.setQueryData(['assets', 'wall'], {
      pages: [{ items: [], next_cursor: '' }],
      pageParams: [''],
    })
    const asset = {
      id: 'asset',
      kind: 'generation',
      job_id: 'job',
      batch_id: 'batch',
      output_index: 0,
    } as Asset
    const event = {
      id: 1,
      type: 'job.succeeded',
      batch_id: 'batch',
      job_id: 'job',
      payload: {
        status: 'succeeded',
        outputs: [],
        assets: [asset],
      },
    }

    applyGenerationEvent(client, event)
    applyGenerationEvent(client, event)

    const generations = client.getQueryData<any>(['generations'])
    const assets = client.getQueryData<any>(['assets', 'wall'])
    expect(generations.pages[0].items[0].jobs[0].status).toBe('succeeded')
    expect(assets.pages[0].items).toEqual([asset])
  })
})

describe('failed job actions', () => {
  it('separates safe manual retries from edit and uncertain flows', () => {
    expect(
      failedJobAction({
        status: 'failed',
        retryable: true,
      } as GenerationBatch['jobs'][number]),
    ).toBe('retry')
    expect(
      failedJobAction({
        status: 'failed',
        retryable: false,
      } as GenerationBatch['jobs'][number]),
    ).toBe('edit')
    expect(
      failedJobAction({
        status: 'submission_uncertain',
      } as GenerationBatch['jobs'][number]),
    ).toBe('none')
  })
})
