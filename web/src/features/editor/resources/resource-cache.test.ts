import { describe, expect, it } from 'vitest'

import { ReferenceCountedResourceCache } from './resource-cache'

describe('ReferenceCountedResourceCache', () => {
  it('coalesces loads and reference-counts shared resources', async () => {
    let loads = 0
    const disposed: string[] = []
    const cache = new ReferenceCountedResourceCache<string>((value) =>
      disposed.push(value),
    )
    const load = async () => {
      loads += 1
      return 'texture'
    }
    const [first, second] = await Promise.all([
      cache.retain('/640', 64, load),
      cache.retain('/640', 64, load),
    ])
    expect([first, second]).toEqual(['texture', 'texture'])
    expect(loads).toBe(1)
    expect(cache.stats()).toEqual({
      entries: 1,
      activeEntries: 1,
      bytes: 64,
      activeBytes: 64,
    })
    cache.release('/640')
    cache.prune(0)
    expect(disposed).toEqual([])
    cache.release('/640')
    cache.prune(0)
    expect(disposed).toEqual(['texture'])
  })

  it('evicts least-recently-used idle resources without active eviction', async () => {
    const disposed: string[] = []
    const cache = new ReferenceCountedResourceCache<string>((value) =>
      disposed.push(value),
    )
    await cache.retain('a', 40, async () => 'a')
    cache.release('a')
    await cache.retain('b', 40, async () => 'b')
    cache.release('b')
    await cache.retain('a', 40, async () => 'a')
    cache.release('a')
    await cache.retain('active', 80, async () => 'active')
    cache.prune(120)
    expect(disposed).toEqual(['b'])
    expect(cache.stats()).toEqual({
      entries: 2,
      activeEntries: 1,
      bytes: 120,
      activeBytes: 80,
    })
  })
})
