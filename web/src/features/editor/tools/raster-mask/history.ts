import type { RasterMaskBuffer, RasterMaskPatch } from './tile-mask'

export class RasterMaskHistory {
  readonly #maxEntries: number
  readonly #maxBytes: number
  #past: RasterMaskPatch[] = []
  #future: RasterMaskPatch[] = []
  #retainedBytes = 0

  constructor(options: { maxEntries?: number; maxBytes?: number } = {}) {
    this.#maxEntries = options.maxEntries ?? 100
    this.#maxBytes = options.maxBytes ?? 64 * 1024 * 1024
    if (!Number.isInteger(this.#maxEntries) || this.#maxEntries < 1)
      throw new RangeError('raster mask history maxEntries must be positive')
    if (!Number.isInteger(this.#maxBytes) || this.#maxBytes < 1)
      throw new RangeError('raster mask history maxBytes must be positive')
  }

  get canUndo() {
    return this.#past.length > 0
  }

  get canRedo() {
    return this.#future.length > 0
  }

  get retainedBytes() {
    return this.#retainedBytes
  }

  commit(patch: RasterMaskPatch) {
    if (patch.tiles.length === 0) return false
    this.#discard(this.#future)
    this.#future = []
    if (patch.byteSize > this.#maxBytes) {
      this.clear()
      return false
    }
    this.#past.push(patch)
    this.#retainedBytes += patch.byteSize
    while (
      this.#past.length > this.#maxEntries ||
      this.#retainedBytes > this.#maxBytes
    ) {
      const removed = this.#past.shift()
      if (removed) this.#retainedBytes -= removed.byteSize
    }
    return true
  }

  undo(buffer: RasterMaskBuffer) {
    const patch = this.#past.pop()
    if (!patch) return undefined
    buffer.applyPatch(patch, 'backward')
    this.#future.push(patch)
    return patch
  }

  redo(buffer: RasterMaskBuffer) {
    const patch = this.#future.pop()
    if (!patch) return undefined
    buffer.applyPatch(patch, 'forward')
    this.#past.push(patch)
    return patch
  }

  clear() {
    this.#past = []
    this.#future = []
    this.#retainedBytes = 0
  }

  #discard(patches: RasterMaskPatch[]) {
    for (const patch of patches) this.#retainedBytes -= patch.byteSize
  }
}
