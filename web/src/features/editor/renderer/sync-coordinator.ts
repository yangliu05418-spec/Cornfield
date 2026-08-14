export type LatestSyncTask<TDocument, TAssets> = {
  document: TDocument
  assets: TAssets
}

type Waiter = {
  resolve: () => void
  reject: (error: unknown) => void
}

type Pending<TDocument, TAssets> = LatestSyncTask<TDocument, TAssets> & {
  waiters: Waiter[]
}

export class LatestSyncCoordinator<TDocument, TAssets> {
  #pending?: Pending<TDocument, TAssets>
  #running = false
  #closed = false
  #passes = 0
  #coalesced = 0

  constructor(
    private readonly execute: (
      document: TDocument,
      assets: TAssets,
    ) => Promise<void>,
  ) {}

  enqueue(document: TDocument, assets: TAssets) {
    if (this.#closed) return Promise.reject(new Error('coordinator is closed'))
    return new Promise<void>((resolve, reject) => {
      const waiter = { resolve, reject }
      if (this.#pending) {
        this.#pending.document = document
        this.#pending.assets = assets
        this.#pending.waiters.push(waiter)
        this.#coalesced += 1
      } else this.#pending = { document, assets, waiters: [waiter] }
      void this.#drain()
    })
  }

  stats() {
    return { passes: this.#passes, coalesced: this.#coalesced }
  }

  close() {
    this.#closed = true
    const pending = this.#pending
    this.#pending = undefined
    if (!pending) return
    const error = new Error('coordinator is closed')
    for (const waiter of pending.waiters) waiter.reject(error)
  }

  async #drain() {
    if (this.#running) return
    this.#running = true
    try {
      while (this.#pending) {
        const pending = this.#pending
        this.#pending = undefined
        try {
          await this.execute(pending.document, pending.assets)
          this.#passes += 1
          for (const waiter of pending.waiters) waiter.resolve()
        } catch (error) {
          for (const waiter of pending.waiters) waiter.reject(error)
        }
      }
    } finally {
      this.#running = false
      if (this.#pending) void this.#drain()
    }
  }
}
