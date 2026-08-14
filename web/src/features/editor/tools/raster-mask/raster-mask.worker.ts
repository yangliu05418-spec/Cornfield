/// <reference lib="webworker" />

import { RasterMaskSession } from './session'
import type {
  RasterMaskWorkerRequest,
  RasterMaskWorkerResponse,
  RasterMaskWorkerResult,
} from './worker-protocol'

const scope = self as DedicatedWorkerGlobalScope
let session: RasterMaskSession | undefined

scope.onmessage = (event: MessageEvent<RasterMaskWorkerRequest>) => {
  const request = event.data
  try {
    const result = handle(request)
    const response: RasterMaskWorkerResponse = {
      requestId: request.requestId,
      ok: true,
      result,
    }
    scope.postMessage(response, transferables(result))
  } catch (error) {
    const response: RasterMaskWorkerResponse = {
      requestId: request.requestId,
      ok: false,
      error: workerError(error),
    }
    scope.postMessage(response)
  }
}

function handle(request: RasterMaskWorkerRequest): RasterMaskWorkerResult {
  if (request.type === 'create') {
    if (session) throw new Error('raster mask session already exists')
    session = new RasterMaskSession(request.width, request.height, {
      defaultAlpha: request.defaultAlpha,
      historyEntries: request.historyEntries,
      historyBytes: request.historyBytes,
    })
    return { created: true, tileSize: session.buffer.tileSize }
  }
  const current = requireSession()
  switch (request.type) {
    case 'begin-stroke':
      return current.beginStroke(request.strokeId, request.brush, request.point)
    case 'add-points':
      if (request.points.length > 4096)
        throw new RangeError('too many raster mask points')
      return current.addPoints(request.strokeId, request.points)
    case 'commit-stroke':
      return current.commitStroke(request.strokeId)
    case 'cancel-stroke':
      return current.cancelStroke(request.strokeId)
    case 'undo':
      return current.undo()
    case 'redo':
      return current.redo()
    case 'hydrate':
      if (request.tiles.length > 1024)
        throw new RangeError('too many raster mask tiles')
      return current.hydrate(request.tiles)
    case 'snapshot':
      if (request.coordinates.length > 1024)
        throw new RangeError('too many raster mask tiles')
      return { tiles: current.snapshot(request.coordinates) }
  }
}

function requireSession() {
  if (!session) throw new Error('raster mask session has not been created')
  return session
}

function transferables(result: RasterMaskWorkerResult) {
  if ('tiles' in result)
    return result.tiles.map((tile) => tile.alpha.buffer) as ArrayBuffer[]
  return []
}

function workerError(error: unknown) {
  if (error instanceof RangeError)
    return { code: 'RASTER_MASK_RANGE_INVALID', message: error.message }
  if (error instanceof TypeError)
    return { code: 'RASTER_MASK_INPUT_INVALID', message: error.message }
  return {
    code: 'RASTER_MASK_STATE_INVALID',
    message:
      error instanceof Error ? error.message : 'raster mask worker failed',
  }
}
