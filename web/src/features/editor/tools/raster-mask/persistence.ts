import { api } from '#/lib/api'

import type { RasterMaskTileSnapshot } from './tile-mask'

export type RasterMaskResource = {
  id: string
  editor_project_id: string
  target_node_id: string
  width: number
  height: number
  default_alpha: number
  current_version: number
  project_revision: number
}

export type RasterMaskVersionTile = {
  tile_x: number
  tile_y: number
  width: number
  height: number
  sha256: string
  byte_size: number
  url: string
}

export type RasterMaskVersion = {
  mask: RasterMaskResource
  version: number
  tiles: RasterMaskVersionTile[]
}

export function createRasterMaskResource(input: {
  projectID: string
  expectedRevision: number
  targetNodeID: string
  defaultAlpha?: number
}) {
  return api<RasterMaskResource>(
    `/api/v1/editor-projects/${input.projectID}/raster-masks`,
    {
      method: 'POST',
      body: JSON.stringify({
        expected_revision: input.expectedRevision,
        target_node_id: input.targetNodeID,
        default_alpha: input.defaultAlpha ?? 255,
      }),
    },
  )
}

export function loadRasterMaskVersion(
  projectID: string,
  maskID: string,
  version: number,
) {
  return api<RasterMaskVersion>(
    `/api/v1/editor-projects/${projectID}/raster-masks/${maskID}/versions/${version}`,
  )
}

export async function loadRasterMaskTiles(
  version: RasterMaskVersion,
  concurrency = 6,
) {
  const snapshots = new Array<RasterMaskTileSnapshot>(version.tiles.length)
  let cursor = 0
  await Promise.all(
    Array.from(
      { length: Math.min(Math.max(1, concurrency), version.tiles.length) },
      async () => {
        for (;;) {
          const index = cursor++
          if (index >= version.tiles.length) return
          const tile = version.tiles[index]
          const response = await fetch(tile.url, { credentials: 'same-origin' })
          if (!response.ok)
            throw new Error(`读取像素蒙版失败（${response.status}）`)
          const alpha = new Uint8Array(await response.arrayBuffer())
          if (alpha.byteLength !== tile.byte_size)
            throw new Error('像素蒙版数据不完整')
          snapshots[index] = {
            tileX: tile.tile_x,
            tileY: tile.tile_y,
            width: tile.width,
            height: tile.height,
            alpha,
          }
        }
      },
    ),
  )
  return snapshots
}

export function commitRasterMaskVersion(input: {
  projectID: string
  maskID: string
  expectedProjectRevision: number
  expectedMaskVersion: number
  defaultAlpha: number
  tiles: readonly RasterMaskTileSnapshot[]
}) {
  const form = new FormData()
  const changes = input.tiles.map((tile) => {
    const isDefault = tile.alpha.every((value) => value === input.defaultAlpha)
    const part = `tile-${tile.tileX}-${tile.tileY}`
    if (!isDefault)
      form.append(
        part,
        new Blob([tile.alpha.slice().buffer], {
          type: 'application/vnd.cornfield.alpha8',
        }),
        `${part}.a8`,
      )
    return {
      tile_x: tile.tileX,
      tile_y: tile.tileY,
      width: tile.width,
      height: tile.height,
      action: isDefault ? 'delete' : 'put',
      ...(isDefault ? {} : { part }),
    }
  })
  form.append(
    'manifest',
    new Blob(
      [
        JSON.stringify({
          expected_project_revision: input.expectedProjectRevision,
          expected_mask_version: input.expectedMaskVersion,
          changes,
        }),
      ],
      { type: 'application/json' },
    ),
  )
  // The server requires the manifest to be the first multipart part. Rebuild
  // after deriving changes so the browser preserves that insertion order.
  const ordered = new FormData()
  ordered.append('manifest', form.get('manifest')!)
  for (const [name, value] of form.entries())
    if (name !== 'manifest') ordered.append(name, value)
  return api<RasterMaskResource>(
    `/api/v1/editor-projects/${input.projectID}/raster-masks/${input.maskID}/versions`,
    { method: 'POST', body: ordered },
  )
}
