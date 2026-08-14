import type { EditorDocumentV1 } from './document'
import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'
import {
  migrateEditorDocumentV1ToV2,
  validateEditorDocumentV2,
} from './document-v2'

export type EditorArtboardV3 = {
  id: string
  name: string
  order_key: string
  x: number
  y: number
  width: number
  height: number
  visible: boolean
  locked: boolean
  nodes: EditorNodeV2[]
}

export type EditorDocumentV3 = {
  schema_version: 3
  renderer_semantics_version: 2
  active_artboard_id: string
  artboards: EditorArtboardV3[]
}

export type EditorAnyDocument =
  EditorDocumentV1 | EditorDocumentV2 | EditorDocumentV3

const maximumArtboards = 32
const maximumNodes = 500
const coordinateLimit = 1_000_000

export function migrateEditorDocumentToV3(
  document: EditorDocumentV1 | EditorDocumentV2,
): EditorDocumentV3 {
  const source =
    document.schema_version === 1
      ? migrateEditorDocumentV1ToV2(document)
      : structuredClone(document)
  const id = 'artboard-1'
  return {
    schema_version: 3,
    renderer_semantics_version: 2,
    active_artboard_id: id,
    artboards: [
      {
        id,
        name: '画板 1',
        order_key: '000001',
        x: 0,
        y: 0,
        width: source.canvas.width,
        height: source.canvas.height,
        visible: true,
        locked: false,
        nodes: structuredClone(source.nodes),
      },
    ],
  }
}

export function activeEditorArtboard(document: EditorDocumentV3) {
  return (
    document.artboards.find(
      (artboard) => artboard.id === document.active_artboard_id,
    ) ?? document.artboards[0]
  )
}

export function artboardAsDocumentV2(
  artboard: EditorArtboardV3,
): EditorDocumentV2 {
  return {
    schema_version: 2,
    renderer_semantics_version: 1,
    canvas: { width: artboard.width, height: artboard.height },
    nodes: artboard.nodes,
  }
}

export function replaceEditorArtboard(
  document: EditorDocumentV3,
  artboardID: string,
  next: EditorDocumentV2,
): EditorDocumentV3 {
  return {
    ...document,
    artboards: document.artboards.map((artboard) =>
      artboard.id === artboardID
        ? {
            ...artboard,
            width: next.canvas.width,
            height: next.canvas.height,
            nodes: next.nodes,
          }
        : artboard,
    ),
  }
}

export function validateEditorDocumentV3(document: EditorDocumentV3) {
  const failures: string[] = []
  if (
    document.artboards.length < 1 ||
    document.artboards.length > maximumArtboards
  )
    failures.push('artboards')
  const artboardIDs = new Set<string>()
  const nodeIDs = new Set<string>()
  let nodeCount = 0
  for (const artboard of document.artboards) {
    if (!artboard.id || artboardIDs.has(artboard.id))
      failures.push(`artboard:${artboard.id || 'missing'}`)
    artboardIDs.add(artboard.id)
    if (
      !Number.isFinite(artboard.x) ||
      !Number.isFinite(artboard.y) ||
      Math.abs(artboard.x) > coordinateLimit ||
      Math.abs(artboard.y) > coordinateLimit
    )
      failures.push(`position:${artboard.id}`)
    const pixels = artboard.width * artboard.height
    if (
      !Number.isInteger(artboard.width) ||
      !Number.isInteger(artboard.height) ||
      artboard.width < 1 ||
      artboard.height < 1 ||
      artboard.width > 8192 ||
      artboard.height > 8192 ||
      pixels > 36_000_000
    )
      failures.push(`size:${artboard.id}`)
    if (artboard.nodes.length) {
      const nested = artboardAsDocumentV2(artboard)
      failures.push(
        ...validateEditorDocumentV2(nested).map(
          (failure) => `${artboard.id}:${failure}`,
        ),
      )
    }
    for (const node of artboard.nodes) {
      nodeCount += 1
      if (nodeIDs.has(node.id)) failures.push(`node:${node.id}`)
      nodeIDs.add(node.id)
    }
  }
  if (nodeCount > maximumNodes) failures.push('nodes')
  if (!artboardIDs.has(document.active_artboard_id))
    failures.push('active_artboard_id')
  return failures
}

export function nextArtboardOrderKey(document: EditorDocumentV3) {
  return String(document.artboards.length + 1).padStart(6, '0')
}

export function createBlankEditorArtboard(
  document: EditorDocumentV3,
  input: { name?: string; width: number; height: number },
): EditorArtboardV3 {
  const right = Math.max(
    ...document.artboards.map((artboard) => artboard.x + artboard.width),
  )
  return {
    id: crypto.randomUUID(),
    name: input.name || `画板 ${document.artboards.length + 1}`,
    order_key: nextArtboardOrderKey(document),
    x: right + 160,
    y: Math.min(...document.artboards.map((artboard) => artboard.y)),
    width: input.width,
    height: input.height,
    visible: true,
    locked: false,
    nodes: [],
  }
}
