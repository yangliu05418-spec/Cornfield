import { afterEach, describe, expect, it, vi } from 'vitest'

import { commitRasterMaskVersion, loadRasterMaskTiles } from './persistence'

afterEach(() => vi.unstubAllGlobals())

describe('raster mask persistence', () => {
  it('sends the manifest first and omits default tile bytes', async () => {
    const fetchMock = vi.fn(async (_path: string, init?: RequestInit) => {
      const form = init?.body as FormData
      const entries = [...form.entries()]
      expect(entries.map(([name]) => name)).toEqual(['manifest', 'tile-0-0'])
      const manifest = JSON.parse(await (entries[0][1] as Blob).text())
      expect(manifest).toEqual({
        expected_project_revision: 8,
        expected_mask_version: 3,
        changes: [
          {
            tile_x: 0,
            tile_y: 0,
            width: 2,
            height: 2,
            action: 'put',
            part: 'tile-0-0',
          },
          {
            tile_x: 1,
            tile_y: 0,
            width: 1,
            height: 2,
            action: 'delete',
          },
        ],
      })
      return new Response(
        JSON.stringify({
          id: 'mask',
          editor_project_id: 'project',
          target_node_id: 'node',
          width: 3,
          height: 2,
          default_alpha: 255,
          current_version: 4,
          project_revision: 9,
        }),
        { status: 201, headers: { 'Content-Type': 'application/json' } },
      )
    })
    vi.stubGlobal('fetch', fetchMock)

    const response = await commitRasterMaskVersion({
      projectID: 'project',
      maskID: 'mask',
      expectedProjectRevision: 8,
      expectedMaskVersion: 3,
      defaultAlpha: 255,
      tiles: [
        {
          tileX: 0,
          tileY: 0,
          width: 2,
          height: 2,
          alpha: new Uint8Array([255, 0, 255, 255]),
        },
        {
          tileX: 1,
          tileY: 0,
          width: 1,
          height: 2,
          alpha: new Uint8Array([255, 255]),
        },
      ],
    })
    expect(response.current_version).toBe(4)
  })

  it('loads immutable alpha tiles with bounded workers and verifies size', async () => {
    let active = 0
    let peak = 0
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        active++
        peak = Math.max(peak, active)
        await Promise.resolve()
        active--
        return new Response(new Uint8Array([1, 2, 3, 4]))
      }),
    )
    const tiles = await loadRasterMaskTiles(
      {
        mask: {
          id: 'mask',
          editor_project_id: 'project',
          target_node_id: 'node',
          width: 4,
          height: 4,
          default_alpha: 255,
          current_version: 1,
          project_revision: 2,
        },
        version: 1,
        tiles: Array.from({ length: 8 }, (_, index) => ({
          tile_x: index,
          tile_y: 0,
          width: 2,
          height: 2,
          sha256: 'a'.repeat(64),
          byte_size: 4,
          url: `/tile/${index}`,
        })),
      },
      3,
    )
    expect(tiles).toHaveLength(8)
    expect(peak).toBeLessThanOrEqual(3)
    expect([...tiles[0].alpha]).toEqual([1, 2, 3, 4])
  })
})
