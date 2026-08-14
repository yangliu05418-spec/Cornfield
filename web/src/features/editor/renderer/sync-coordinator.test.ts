import { describe, expect, it, vi } from 'vitest'

import { LatestSyncCoordinator } from './sync-coordinator'

describe('LatestSyncCoordinator', () => {
  it('preserves the running pass and coalesces pending work to the latest value', async () => {
    let release!: () => void
    const first = new Promise<void>((resolve) => {
      release = resolve
    })
    const execute = vi
      .fn<(document: number, assets: string) => Promise<void>>()
      .mockImplementationOnce(() => first)
      .mockResolvedValue(undefined)
    const coordinator = new LatestSyncCoordinator(execute)
    const tasks = [
      coordinator.enqueue(1, 'a'),
      coordinator.enqueue(2, 'b'),
      coordinator.enqueue(3, 'c'),
      coordinator.enqueue(4, 'd'),
    ]
    await Promise.resolve()
    expect(execute).toHaveBeenCalledTimes(1)
    release()
    await Promise.all(tasks)
    expect(execute.mock.calls).toEqual([
      [1, 'a'],
      [4, 'd'],
    ])
    expect(coordinator.stats()).toEqual({ passes: 2, coalesced: 2 })
  })

  it('rejects pending and future work when closed', async () => {
    let release!: () => void
    const first = new Promise<void>((resolve) => {
      release = resolve
    })
    const coordinator = new LatestSyncCoordinator<number, string>(() => first)
    const running = coordinator.enqueue(1, 'a')
    const pending = coordinator.enqueue(2, 'b')
    coordinator.close()
    await expect(pending).rejects.toThrow('coordinator is closed')
    await expect(coordinator.enqueue(3, 'c')).rejects.toThrow(
      'coordinator is closed',
    )
    release()
    await expect(running).resolves.toBeUndefined()
  })
})
