import { RasterMaskHistory } from './history'
import type {
  RasterMaskBrush,
  RasterMaskPatch,
  RasterMaskPoint,
  RasterMaskStroke,
  RasterMaskTileSnapshot,
} from './tile-mask'
import { RasterMaskBuffer } from './tile-mask'

export type RasterMaskMutation = {
  tiles: RasterMaskTileSnapshot[]
  changedPixels: number
  canUndo: boolean
  canRedo: boolean
  retainedHistoryBytes: number
  undoRetained: boolean
}

export class RasterMaskSession {
  readonly buffer: RasterMaskBuffer
  readonly history: RasterMaskHistory
  #stroke: { id: string; value: RasterMaskStroke } | undefined

  constructor(
    width: number,
    height: number,
    options: {
      defaultAlpha?: number
      historyEntries?: number
      historyBytes?: number
    } = {},
  ) {
    this.buffer = new RasterMaskBuffer(width, height, options.defaultAlpha)
    this.history = new RasterMaskHistory({
      maxEntries: options.historyEntries,
      maxBytes: options.historyBytes,
    })
  }

  beginStroke(id: string, brush: RasterMaskBrush, point: RasterMaskPoint) {
    if (!id || id.length > 128) throw new RangeError('invalid stroke id')
    if (this.#stroke) throw new Error('a raster mask stroke is already active')
    this.#stroke = { id, value: this.buffer.beginStroke(brush, point) }
    return this.#status(this.#stroke.value.takeDirtyTiles())
  }

  addPoints(id: string, points: readonly RasterMaskPoint[]) {
    const stroke = this.#requireStroke(id)
    for (const point of points) stroke.add(point)
    return this.#status(stroke.takeDirtyTiles())
  }

  commitStroke(id: string) {
    const stroke = this.#requireStroke(id)
    stroke.takeDirtyTiles()
    this.#stroke = undefined
    const patch = stroke.commit()
    const undoRetained = this.history.commit(patch)
    return this.#status(
      this.buffer.snapshotTiles(
        patch.tiles.map(({ tileX, tileY }) => ({ tileX, tileY })),
      ),
      patch.changedPixels,
      undoRetained,
    )
  }

  cancelStroke(id: string) {
    const stroke = this.#requireStroke(id)
    const coordinates = stroke.touchedCoordinates()
    this.#stroke = undefined
    stroke.cancel()
    return this.#status(this.buffer.snapshotTiles(coordinates))
  }

  undo() {
    this.#requireIdle()
    const patch = this.history.undo(this.buffer)
    return patch ? this.#mutation(patch, true) : this.#status([])
  }

  redo() {
    this.#requireIdle()
    const patch = this.history.redo(this.buffer)
    return patch ? this.#mutation(patch, true) : this.#status([])
  }

  snapshot(coordinates: ReadonlyArray<{ tileX: number; tileY: number }>) {
    return this.buffer.snapshotTiles(coordinates)
  }

  hydrate(tiles: readonly RasterMaskTileSnapshot[]) {
    this.#requireIdle()
    this.buffer.replaceAllTiles(tiles)
    this.history.clear()
    return this.#status(this.buffer.snapshotTiles(tiles))
  }

  #mutation(patch: RasterMaskPatch, undoRetained: boolean) {
    const coordinates = patch.tiles.map(({ tileX, tileY }) => ({
      tileX,
      tileY,
    }))
    return this.#status(
      this.buffer.snapshotTiles(coordinates),
      patch.changedPixels,
      undoRetained,
    )
  }

  #status(
    tiles: RasterMaskTileSnapshot[],
    changedPixels = 0,
    undoRetained = true,
  ): RasterMaskMutation {
    return {
      tiles,
      changedPixels,
      canUndo: this.history.canUndo,
      canRedo: this.history.canRedo,
      retainedHistoryBytes: this.history.retainedBytes,
      undoRetained,
    }
  }

  #requireStroke(id: string) {
    if (!this.#stroke || this.#stroke.id !== id)
      throw new Error('raster mask stroke does not match the active gesture')
    return this.#stroke.value
  }

  #requireIdle() {
    if (this.#stroke) throw new Error('cannot change history during a stroke')
  }
}
