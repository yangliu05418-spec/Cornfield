import type { RasterMaskBrush, RasterMaskPoint } from './tile-mask'
import type {
  RasterMaskWorkerCreated,
  RasterMaskWorkerRequest,
  RasterMaskWorkerResponse,
  RasterMaskWorkerResult,
  RasterMaskWorkerSnapshot,
} from './worker-protocol'
import type { RasterMaskMutation } from './session'

type WorkerPort = Pick<
  Worker,
  'postMessage' | 'terminate' | 'onmessage' | 'onerror'
>

type WithoutRequestID<T> = T extends { requestId: number }
  ? Omit<T, 'requestId'>
  : never
type RasterMaskWorkerCommand = WithoutRequestID<RasterMaskWorkerRequest>

export class RasterMaskWorkerError extends Error {
  readonly code: string

  constructor(code: string, message: string) {
    super(message)
    this.name = 'RasterMaskWorkerError'
    this.code = code
  }
}

export class RasterMaskWorkerClient {
  readonly #worker: WorkerPort
  readonly #pending = new Map<
    number,
    {
      resolve: (result: RasterMaskWorkerResult) => void
      reject: (error: Error) => void
    }
  >()
  #nextRequestID = 1
  #closed = false

  constructor(worker: WorkerPort) {
    this.#worker = worker
    worker.onmessage = (event: MessageEvent<RasterMaskWorkerResponse>) => {
      const response = event.data
      const pending = this.#pending.get(response.requestId)
      if (!pending) return
      this.#pending.delete(response.requestId)
      if (response.ok) pending.resolve(response.result)
      else
        pending.reject(
          new RasterMaskWorkerError(
            response.error.code,
            response.error.message,
          ),
        )
    }
    worker.onerror = () => {
      this.#closed = true
      this.#worker.terminate()
      this.#failAll(
        new RasterMaskWorkerError(
          'RASTER_MASK_WORKER_FAILED',
          '蒙版画笔进程已停止',
        ),
      )
    }
  }

  create(
    width: number,
    height: number,
    options: {
      defaultAlpha?: number
      historyEntries?: number
      historyBytes?: number
    } = {},
  ): Promise<RasterMaskWorkerCreated> {
    return this.#request<RasterMaskWorkerCreated>({
      type: 'create',
      width,
      height,
      ...options,
    })
  }

  beginStroke(
    strokeId: string,
    brush: RasterMaskBrush,
    point: RasterMaskPoint,
  ): Promise<RasterMaskMutation> {
    return this.#request<RasterMaskMutation>({
      type: 'begin-stroke',
      strokeId,
      brush,
      point,
    })
  }

  addPoints(
    strokeId: string,
    points: RasterMaskPoint[],
  ): Promise<RasterMaskMutation> {
    return this.#request<RasterMaskMutation>({
      type: 'add-points',
      strokeId,
      points,
    })
  }

  commitStroke(strokeId: string): Promise<RasterMaskMutation> {
    return this.#request<RasterMaskMutation>({
      type: 'commit-stroke',
      strokeId,
    })
  }

  cancelStroke(strokeId: string): Promise<RasterMaskMutation> {
    return this.#request<RasterMaskMutation>({
      type: 'cancel-stroke',
      strokeId,
    })
  }

  undo(): Promise<RasterMaskMutation> {
    return this.#request<RasterMaskMutation>({ type: 'undo' })
  }

  redo(): Promise<RasterMaskMutation> {
    return this.#request<RasterMaskMutation>({ type: 'redo' })
  }

  snapshot(
    coordinates: Array<{ tileX: number; tileY: number }>,
  ): Promise<RasterMaskWorkerSnapshot> {
    return this.#request<RasterMaskWorkerSnapshot>({
      type: 'snapshot',
      coordinates,
    })
  }

  close() {
    if (this.#closed) return
    this.#closed = true
    this.#worker.terminate()
    this.#failAll(
      new RasterMaskWorkerError(
        'RASTER_MASK_WORKER_CLOSED',
        '蒙版画笔进程已关闭',
      ),
    )
  }

  #request<TResult extends RasterMaskWorkerResult>(
    request: RasterMaskWorkerCommand,
  ) {
    if (this.#closed)
      return Promise.reject(
        new RasterMaskWorkerError(
          'RASTER_MASK_WORKER_CLOSED',
          '蒙版画笔进程已关闭',
        ),
      )
    const requestId = this.#nextRequestID++
    return new Promise<TResult>((resolve, reject) => {
      this.#pending.set(requestId, {
        resolve: (result) => resolve(result as TResult),
        reject,
      })
      try {
        this.#worker.postMessage({ ...request, requestId })
      } catch (error) {
        this.#pending.delete(requestId)
        reject(
          error instanceof Error
            ? error
            : new RasterMaskWorkerError(
                'RASTER_MASK_WORKER_MESSAGE_FAILED',
                '无法发送蒙版画笔指令',
              ),
        )
      }
    })
  }

  #failAll(error: Error) {
    for (const pending of this.#pending.values()) pending.reject(error)
    this.#pending.clear()
  }
}

export function createRasterMaskWorkerClient() {
  return new RasterMaskWorkerClient(
    new Worker(new URL('./raster-mask.worker.ts', import.meta.url), {
      type: 'module',
      name: 'cornfield-raster-mask',
    }),
  )
}
