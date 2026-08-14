import { describe, expect, it } from 'vitest'

import { RasterMaskWorkerClient, RasterMaskWorkerError } from './worker-client'
import type {
  RasterMaskWorkerRequest,
  RasterMaskWorkerResponse,
} from './worker-protocol'

class FakeWorker {
  onmessage: ((event: MessageEvent<RasterMaskWorkerResponse>) => void) | null =
    null
  onerror: ((event: ErrorEvent) => void) | null = null
  requests: RasterMaskWorkerRequest[] = []
  terminated = false

  postMessage(request: RasterMaskWorkerRequest) {
    this.requests.push(request)
  }

  terminate() {
    this.terminated = true
  }

  respond(response: RasterMaskWorkerResponse) {
    this.onmessage?.({
      data: response,
    } as MessageEvent<RasterMaskWorkerResponse>)
  }
}

describe('RasterMaskWorkerClient', () => {
  it('correlates out-of-order replies without leaking pending requests', async () => {
    const worker = new FakeWorker()
    const client = new RasterMaskWorkerClient(worker)
    const first = client.create(1024, 1024)
    const second = client.undo()
    const [createRequest, undoRequest] = worker.requests

    worker.respond({
      requestId: undoRequest.requestId,
      ok: true,
      result: {
        tiles: [],
        changedPixels: 0,
        canUndo: false,
        canRedo: false,
        retainedHistoryBytes: 0,
        undoRetained: true,
      },
    })
    worker.respond({
      requestId: createRequest.requestId,
      ok: true,
      result: { created: true, tileSize: 256 },
    })

    await expect(first).resolves.toEqual({ created: true, tileSize: 256 })
    await expect(second).resolves.toMatchObject({ canUndo: false })
  })

  it('returns typed worker errors and rejects work after close', async () => {
    const worker = new FakeWorker()
    const client = new RasterMaskWorkerClient(worker)
    const pending = client.create(1, 1)
    const request = worker.requests[0]
    worker.respond({
      requestId: request.requestId,
      ok: false,
      error: { code: 'RASTER_MASK_RANGE_INVALID', message: 'bad size' },
    })
    await expect(pending).rejects.toMatchObject({
      name: 'RasterMaskWorkerError',
      code: 'RASTER_MASK_RANGE_INVALID',
    })

    client.close()
    expect(worker.terminated).toBe(true)
    await expect(client.undo()).rejects.toBeInstanceOf(RasterMaskWorkerError)
  })

  it('rejects all in-flight work if the worker fails', async () => {
    const worker = new FakeWorker()
    const client = new RasterMaskWorkerClient(worker)
    const first = client.create(512, 512)
    const second = client.undo()
    worker.onerror?.({} as ErrorEvent)
    await expect(first).rejects.toMatchObject({
      code: 'RASTER_MASK_WORKER_FAILED',
    })
    await expect(second).rejects.toMatchObject({
      code: 'RASTER_MASK_WORKER_FAILED',
    })
  })
})
