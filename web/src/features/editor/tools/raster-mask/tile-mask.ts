export const RASTER_MASK_TILE_SIZE = 256

const MAX_MASK_PIXELS = 36_000_000
const MAX_MASK_EDGE = 8192
const MAX_BRUSH_SIZE = 2048

export type RasterMaskBrush = {
  size: number
  hardness: number
  opacity: number
  spacing: number
  mode: 'paint' | 'erase'
  pressureSize: number
  pressureOpacity: number
}

export type RasterMaskPoint = {
  x: number
  y: number
  pressure?: number
}

export type RasterMaskTilePatch = {
  tileX: number
  tileY: number
  x: number
  y: number
  width: number
  height: number
  before: Uint8Array
  after: Uint8Array
}

export type RasterMaskPatch = {
  tiles: RasterMaskTilePatch[]
  changedPixels: number
  byteSize: number
}

export type RasterMaskTileSnapshot = {
  tileX: number
  tileY: number
  width: number
  height: number
  alpha: Uint8Array
}

type Tile = {
  width: number
  height: number
  data: Uint8Array
}

type TouchedTile = {
  tileX: number
  tileY: number
  before: Uint8Array
  minX: number
  minY: number
  maxX: number
  maxY: number
}

export class RasterMaskBuffer {
  readonly width: number
  readonly height: number
  readonly defaultAlpha: number
  readonly tileSize = RASTER_MASK_TILE_SIZE

  readonly #columns: number
  readonly #tiles = new Map<number, Tile>()
  #activeStroke: RasterMaskStroke | undefined

  constructor(width: number, height: number, defaultAlpha = 255) {
    if (
      !Number.isInteger(width) ||
      !Number.isInteger(height) ||
      width < 1 ||
      height < 1 ||
      width > MAX_MASK_EDGE ||
      height > MAX_MASK_EDGE ||
      width * height > MAX_MASK_PIXELS
    )
      throw new RangeError('invalid raster mask dimensions')
    if (
      !Number.isInteger(defaultAlpha) ||
      defaultAlpha < 0 ||
      defaultAlpha > 255
    )
      throw new RangeError('default alpha must be an integer from 0 to 255')
    this.width = width
    this.height = height
    this.defaultAlpha = defaultAlpha
    this.#columns = Math.ceil(width / this.tileSize)
  }

  get allocatedTileCount() {
    return this.#tiles.size
  }

  get allocatedBytes() {
    let total = 0
    for (const tile of this.#tiles.values()) total += tile.data.byteLength
    return total
  }

  readAlpha(x: number, y: number) {
    if (
      !Number.isInteger(x) ||
      !Number.isInteger(y) ||
      x < 0 ||
      y < 0 ||
      x >= this.width ||
      y >= this.height
    )
      return undefined
    const tileX = Math.floor(x / this.tileSize)
    const tileY = Math.floor(y / this.tileSize)
    const tile = this.#tiles.get(this.#key(tileX, tileY))
    if (!tile) return this.defaultAlpha
    return tile.data[
      (y - tileY * this.tileSize) * tile.width + x - tileX * this.tileSize
    ]
  }

  readTile(tileX: number, tileY: number) {
    const dimensions = this.#tileDimensions(tileX, tileY)
    if (!dimensions) return undefined
    const tile = this.#tiles.get(this.#key(tileX, tileY))
    if (tile) return tile.data.slice()
    const data = new Uint8Array(dimensions.width * dimensions.height)
    data.fill(this.defaultAlpha)
    return data
  }

  snapshotTiles(coordinates: ReadonlyArray<{ tileX: number; tileY: number }>) {
    const snapshots: RasterMaskTileSnapshot[] = []
    const seen = new Set<number>()
    for (const coordinate of coordinates) {
      const key = this.#key(coordinate.tileX, coordinate.tileY)
      if (seen.has(key)) continue
      seen.add(key)
      const dimensions = this.#tileDimensions(
        coordinate.tileX,
        coordinate.tileY,
      )
      if (!dimensions) throw new RangeError('tile is outside the raster mask')
      snapshots.push({
        ...coordinate,
        ...dimensions,
        alpha: this.readTile(coordinate.tileX, coordinate.tileY)!,
      })
    }
    snapshots.sort(
      (left, right) => left.tileY - right.tileY || left.tileX - right.tileX,
    )
    return snapshots
  }

  beginStroke(brush: RasterMaskBrush, point: RasterMaskPoint) {
    if (this.#activeStroke)
      throw new Error('a raster mask stroke is already active')
    validateBrush(brush)
    validatePoint(point)
    const stroke = new RasterMaskStroke(
      this,
      { ...brush },
      normalizePoint(point),
    )
    this.#activeStroke = stroke
    stroke.start()
    return stroke
  }

  applyPatch(patch: RasterMaskPatch, direction: 'forward' | 'backward') {
    if (this.#activeStroke)
      throw new Error('cannot apply a patch during an active stroke')
    for (const change of patch.tiles) {
      const dimensions = this.#tileDimensions(change.tileX, change.tileY)
      if (
        !dimensions ||
        !Number.isInteger(change.x) ||
        !Number.isInteger(change.y) ||
        !Number.isInteger(change.width) ||
        !Number.isInteger(change.height) ||
        change.x < 0 ||
        change.y < 0 ||
        change.width < 1 ||
        change.height < 1 ||
        change.x + change.width > dimensions.width ||
        change.y + change.height > dimensions.height ||
        change.before.length !== change.width * change.height ||
        change.after.length !== change.width * change.height
      )
        throw new TypeError('invalid raster mask patch')
      const tile = this.#mutableTile(change.tileX, change.tileY)
      const source = direction === 'forward' ? change.after : change.before
      writeRegion(tile, change.x, change.y, change.width, change.height, source)
      this.#compact(change.tileX, change.tileY)
    }
  }

  clear() {
    if (this.#activeStroke)
      throw new Error('cannot clear during an active stroke')
    this.#tiles.clear()
  }

  _touch(tileX: number, tileY: number, touched: Map<number, TouchedTile>) {
    const key = this.#key(tileX, tileY)
    let record = touched.get(key)
    if (record) return { tile: this.#mutableTile(tileX, tileY), record }
    const tile = this.#mutableTile(tileX, tileY)
    record = {
      tileX,
      tileY,
      before: tile.data.slice(),
      minX: tile.width,
      minY: tile.height,
      maxX: -1,
      maxY: -1,
    }
    touched.set(key, record)
    return { tile, record }
  }

  _finishStroke(
    stroke: RasterMaskStroke,
    touched: Map<number, TouchedTile>,
    cancelled: boolean,
  ) {
    if (this.#activeStroke !== stroke)
      throw new Error('raster mask stroke is not active')
    this.#activeStroke = undefined
    if (cancelled) {
      for (const record of touched.values()) {
        const tile = this.#mutableTile(record.tileX, record.tileY)
        tile.data.set(record.before)
        this.#compact(record.tileX, record.tileY)
      }
      return emptyPatch()
    }

    const tiles: RasterMaskTilePatch[] = []
    let changedPixels = 0
    for (const record of touched.values()) {
      if (record.maxX < record.minX || record.maxY < record.minY) {
        this.#compact(record.tileX, record.tileY)
        continue
      }
      const tile = this.#mutableTile(record.tileX, record.tileY)
      const width = record.maxX - record.minX + 1
      const height = record.maxY - record.minY + 1
      const before = readRegion(
        tile.width,
        record.before,
        record.minX,
        record.minY,
        width,
        height,
      )
      const after = readRegion(
        tile.width,
        tile.data,
        record.minX,
        record.minY,
        width,
        height,
      )
      if (!arraysEqual(before, after)) {
        tiles.push({
          tileX: record.tileX,
          tileY: record.tileY,
          x: record.minX,
          y: record.minY,
          width,
          height,
          before,
          after,
        })
        for (let index = 0; index < before.length; index++)
          if (before[index] !== after[index]) changedPixels++
      }
      this.#compact(record.tileX, record.tileY)
    }
    tiles.sort(
      (left, right) => left.tileY - right.tileY || left.tileX - right.tileX,
    )
    return {
      tiles,
      changedPixels,
      byteSize: tiles.reduce(
        (total, tile) => total + tile.before.byteLength + tile.after.byteLength,
        0,
      ),
    }
  }

  #mutableTile(tileX: number, tileY: number) {
    const dimensions = this.#tileDimensions(tileX, tileY)
    if (!dimensions) throw new RangeError('tile is outside the raster mask')
    const key = this.#key(tileX, tileY)
    let tile = this.#tiles.get(key)
    if (!tile) {
      const data = new Uint8Array(dimensions.width * dimensions.height)
      data.fill(this.defaultAlpha)
      tile = { ...dimensions, data }
      this.#tiles.set(key, tile)
    }
    return tile
  }

  #compact(tileX: number, tileY: number) {
    const key = this.#key(tileX, tileY)
    const tile = this.#tiles.get(key)
    if (!tile) return
    for (const alpha of tile.data) if (alpha !== this.defaultAlpha) return
    this.#tiles.delete(key)
  }

  #tileDimensions(tileX: number, tileY: number) {
    if (
      !Number.isInteger(tileX) ||
      !Number.isInteger(tileY) ||
      tileX < 0 ||
      tileY < 0
    )
      return undefined
    const left = tileX * this.tileSize
    const top = tileY * this.tileSize
    if (left >= this.width || top >= this.height) return undefined
    return {
      width: Math.min(this.tileSize, this.width - left),
      height: Math.min(this.tileSize, this.height - top),
    }
  }

  #key(tileX: number, tileY: number) {
    return tileY * this.#columns + tileX
  }
}

export class RasterMaskStroke {
  readonly #buffer: RasterMaskBuffer
  readonly #brush: RasterMaskBrush
  readonly #touched = new Map<number, TouchedTile>()
  readonly #dirty = new Map<string, { tileX: number; tileY: number }>()
  #last: Required<RasterMaskPoint>
  #closed = false

  constructor(
    buffer: RasterMaskBuffer,
    brush: RasterMaskBrush,
    first: Required<RasterMaskPoint>,
  ) {
    this.#buffer = buffer
    this.#brush = brush
    this.#last = first
  }

  start() {
    this.#dab(this.#last)
  }

  add(point: RasterMaskPoint) {
    this.#ensureOpen()
    validatePoint(point)
    const next = normalizePoint(point)
    const dx = next.x - this.#last.x
    const dy = next.y - this.#last.y
    const distance = Math.hypot(dx, dy)
    const step = Math.max(0.5, this.#brush.size * this.#brush.spacing)
    const count = Math.max(1, Math.ceil(distance / step))
    for (let index = 1; index <= count; index++) {
      const ratio = index / count
      this.#dab({
        x: this.#last.x + dx * ratio,
        y: this.#last.y + dy * ratio,
        pressure:
          this.#last.pressure + (next.pressure - this.#last.pressure) * ratio,
      })
    }
    this.#last = next
  }

  takeDirtyTiles() {
    this.#ensureOpen()
    const snapshots = this.#buffer.snapshotTiles([...this.#dirty.values()])
    this.#dirty.clear()
    return snapshots
  }

  touchedCoordinates() {
    return [...this.#touched.values()].map(({ tileX, tileY }) => ({
      tileX,
      tileY,
    }))
  }

  commit() {
    this.#ensureOpen()
    this.#closed = true
    return this.#buffer._finishStroke(this, this.#touched, false)
  }

  cancel() {
    this.#ensureOpen()
    this.#closed = true
    return this.#buffer._finishStroke(this, this.#touched, true)
  }

  #dab(point: Required<RasterMaskPoint>) {
    const pressure = clamp(point.pressure, 0, 1)
    const sizeScale =
      1 -
      this.#brush.pressureSize +
      this.#brush.pressureSize * Math.max(pressure, 0.05)
    const opacityScale =
      1 - this.#brush.pressureOpacity + this.#brush.pressureOpacity * pressure
    const radius = Math.max(0.5, (this.#brush.size * sizeScale) / 2)
    const opacity = this.#brush.opacity * opacityScale
    if (opacity <= 0) return
    const innerRadius = radius * this.#brush.hardness
    const left = Math.max(0, Math.floor(point.x - radius))
    const top = Math.max(0, Math.floor(point.y - radius))
    const right = Math.min(
      this.#buffer.width - 1,
      Math.ceil(point.x + radius) - 1,
    )
    const bottom = Math.min(
      this.#buffer.height - 1,
      Math.ceil(point.y + radius) - 1,
    )

    for (let y = top; y <= bottom; y++) {
      const tileY = Math.floor(y / this.#buffer.tileSize)
      const localY = y - tileY * this.#buffer.tileSize
      for (let x = left; x <= right; x++) {
        const distance = Math.hypot(x + 0.5 - point.x, y + 0.5 - point.y)
        if (distance >= radius) continue
        let coverage = 1
        if (distance > innerRadius && innerRadius < radius) {
          const falloff = clamp(
            (radius - distance) / (radius - innerRadius),
            0,
            1,
          )
          coverage = falloff * falloff * (3 - 2 * falloff)
        }
        coverage *= opacity
        if (coverage <= 0) continue
        const tileX = Math.floor(x / this.#buffer.tileSize)
        const localX = x - tileX * this.#buffer.tileSize
        const { tile, record } = this.#buffer._touch(
          tileX,
          tileY,
          this.#touched,
        )
        const offset = localY * tile.width + localX
        const previous = tile.data[offset]
        const next =
          this.#brush.mode === 'paint'
            ? Math.round(previous + (255 - previous) * coverage)
            : Math.round(previous * (1 - coverage))
        if (next === previous) continue
        tile.data[offset] = next
        this.#dirty.set(`${tileX}:${tileY}`, { tileX, tileY })
        record.minX = Math.min(record.minX, localX)
        record.minY = Math.min(record.minY, localY)
        record.maxX = Math.max(record.maxX, localX)
        record.maxY = Math.max(record.maxY, localY)
      }
    }
  }

  #ensureOpen() {
    if (this.#closed) throw new Error('raster mask stroke is already closed')
  }
}

function validateBrush(brush: RasterMaskBrush) {
  const runtimeMode: string = brush.mode
  if (
    !Number.isFinite(brush.size) ||
    brush.size < 1 ||
    brush.size > MAX_BRUSH_SIZE ||
    !unit(brush.hardness) ||
    !unit(brush.opacity) ||
    !Number.isFinite(brush.spacing) ||
    brush.spacing < 0.01 ||
    brush.spacing > 1 ||
    (runtimeMode !== 'paint' && runtimeMode !== 'erase') ||
    !unit(brush.pressureSize) ||
    !unit(brush.pressureOpacity)
  )
    throw new RangeError('invalid raster mask brush')
}

function validatePoint(point: RasterMaskPoint) {
  if (
    !Number.isFinite(point.x) ||
    !Number.isFinite(point.y) ||
    (point.pressure !== undefined && !unit(point.pressure))
  )
    throw new RangeError('invalid raster mask point')
}

function normalizePoint(point: RasterMaskPoint): Required<RasterMaskPoint> {
  return { x: point.x, y: point.y, pressure: point.pressure ?? 1 }
}

function unit(value: number) {
  return Number.isFinite(value) && value >= 0 && value <= 1
}

function clamp(value: number, minimum: number, maximum: number) {
  return Math.min(maximum, Math.max(minimum, value))
}

function readRegion(
  tileWidth: number,
  data: Uint8Array,
  x: number,
  y: number,
  width: number,
  height: number,
) {
  const result = new Uint8Array(width * height)
  for (let row = 0; row < height; row++)
    result.set(
      data.subarray(
        (y + row) * tileWidth + x,
        (y + row) * tileWidth + x + width,
      ),
      row * width,
    )
  return result
}

function writeRegion(
  tile: Tile,
  x: number,
  y: number,
  width: number,
  height: number,
  source: Uint8Array,
) {
  for (let row = 0; row < height; row++)
    tile.data.set(
      source.subarray(row * width, (row + 1) * width),
      (y + row) * tile.width + x,
    )
}

function arraysEqual(left: Uint8Array, right: Uint8Array) {
  if (left.length !== right.length) return false
  for (let index = 0; index < left.length; index++)
    if (left[index] !== right[index]) return false
  return true
}

function emptyPatch(): RasterMaskPatch {
  return { tiles: [], changedPixels: 0, byteSize: 0 }
}
