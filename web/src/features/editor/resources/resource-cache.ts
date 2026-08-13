export type ResourceCacheStats = {
  entries: number
  activeEntries: number
  bytes: number
  activeBytes: number
}

type ResourceRecord<T> = {
  value: T
  bytes: number
  refs: number
  lastUsed: number
}

export class ReferenceCountedResourceCache<T> {
  #records = new Map<string, ResourceRecord<T>>()
  #pending = new Map<string, Promise<ResourceRecord<T>>>()
  #clock = 0
  #disposed = false

  constructor(private readonly dispose: (value: T) => void) {}

  async retain(key: string, bytes: number, load: () => Promise<T>): Promise<T> {
    if (this.#disposed) throw new Error('resource cache is disposed')
    const existing = this.#records.get(key)
    if (existing) {
      existing.refs += 1
      existing.lastUsed = ++this.#clock
      return existing.value
    }
    let pending = this.#pending.get(key)
    if (!pending) {
      pending = load().then((value) => {
        if (this.#disposed) {
          this.dispose(value)
          throw new Error('resource cache is disposed')
        }
        return {
          value,
          bytes,
          refs: 0,
          lastUsed: ++this.#clock,
        }
      })
      this.#pending.set(key, pending)
    }
    let record: ResourceRecord<T>
    try {
      record = await pending
    } finally {
      if (this.#pending.get(key) === pending) this.#pending.delete(key)
    }
    const installed = this.#records.get(key) ?? record
    if (!this.#records.has(key)) this.#records.set(key, installed)
    installed.refs += 1
    installed.lastUsed = ++this.#clock
    return installed.value
  }

  release(key: string) {
    const record = this.#records.get(key)
    if (!record || record.refs === 0) return
    record.refs -= 1
    record.lastUsed = ++this.#clock
  }

  prune(maxBytes: number) {
    let bytes = this.stats().bytes
    if (bytes <= maxBytes) return
    const idle = [...this.#records.entries()]
      .filter(([, record]) => record.refs === 0)
      .sort(
        (left, right) =>
          left[1].lastUsed - right[1].lastUsed ||
          left[0].localeCompare(right[0]),
      )
    for (const [key, record] of idle) {
      if (bytes <= maxBytes) break
      this.#records.delete(key)
      bytes -= record.bytes
      this.dispose(record.value)
    }
  }

  stats(): ResourceCacheStats {
    let bytes = 0
    let activeEntries = 0
    let activeBytes = 0
    for (const record of this.#records.values()) {
      bytes += record.bytes
      if (record.refs > 0) {
        activeEntries += 1
        activeBytes += record.bytes
      }
    }
    return { entries: this.#records.size, activeEntries, bytes, activeBytes }
  }

  clear() {
    if (this.#disposed) return
    this.#disposed = true
    for (const record of this.#records.values()) this.dispose(record.value)
    this.#records.clear()
    this.#pending.clear()
  }
}
