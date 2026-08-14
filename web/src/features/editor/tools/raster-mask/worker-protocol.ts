import type {
  RasterMaskBrush,
  RasterMaskPoint,
  RasterMaskTileSnapshot,
} from './tile-mask'
import type { RasterMaskMutation } from './session'

type RequestBase = { requestId: number }

export type RasterMaskWorkerRequest =
  | (RequestBase & {
      type: 'create'
      width: number
      height: number
      defaultAlpha?: number
      historyEntries?: number
      historyBytes?: number
    })
  | (RequestBase & {
      type: 'begin-stroke'
      strokeId: string
      brush: RasterMaskBrush
      point: RasterMaskPoint
    })
  | (RequestBase & {
      type: 'add-points'
      strokeId: string
      points: RasterMaskPoint[]
    })
  | (RequestBase & { type: 'commit-stroke'; strokeId: string })
  | (RequestBase & { type: 'cancel-stroke'; strokeId: string })
  | (RequestBase & { type: 'undo' })
  | (RequestBase & { type: 'redo' })
  | (RequestBase & {
      type: 'snapshot'
      coordinates: Array<{ tileX: number; tileY: number }>
    })

export type RasterMaskWorkerCreated = { created: true; tileSize: number }
export type RasterMaskWorkerSnapshot = { tiles: RasterMaskTileSnapshot[] }

export type RasterMaskWorkerResult =
  RasterMaskWorkerCreated | RasterMaskMutation | RasterMaskWorkerSnapshot

export type RasterMaskWorkerResponse =
  | {
      requestId: number
      ok: true
      result: RasterMaskWorkerResult
    }
  | {
      requestId: number
      ok: false
      error: { code: string; message: string }
    }
