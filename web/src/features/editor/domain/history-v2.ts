import type { EditorDocumentV2, EditorNodeV2 } from './document-v2'

type NodeChange = {
  id: string
  before?: EditorNodeV2
  after?: EditorNodeV2
}

export type EditorDocumentPatchV2 = {
  canvas?: {
    before: EditorDocumentV2['canvas']
    after: EditorDocumentV2['canvas']
  }
  nodes: NodeChange[]
}

type HistoryEntry = {
  patch: EditorDocumentPatchV2
  mergeKey?: string
}

export class EditorHistoryV2 {
  readonly #limit: number
  #past: HistoryEntry[] = []
  #future: HistoryEntry[] = []

  constructor(limit = 100) {
    if (!Number.isInteger(limit) || limit < 1)
      throw new Error('history limit must be a positive integer')
    this.#limit = limit
  }

  get canUndo() {
    return this.#past.length > 0
  }

  get canRedo() {
    return this.#future.length > 0
  }

  clear() {
    this.#past = []
    this.#future = []
  }

  commit(
    before: EditorDocumentV2,
    after: EditorDocumentV2,
    options: { mergeKey?: string } = {},
  ) {
    if (documentsEqual(before, after)) return false
    const last = this.#past.at(-1)
    if (options.mergeKey && last?.mergeKey === options.mergeKey) {
      const original = applyEditorPatchV2(before, last.patch, 'backward')
      last.patch = diffEditorDocumentsV2(original, after)
    } else {
      this.#past.push({
        patch: diffEditorDocumentsV2(before, after),
        mergeKey: options.mergeKey,
      })
      if (this.#past.length > this.#limit) this.#past.shift()
    }
    this.#future = []
    return true
  }

  undo(document: EditorDocumentV2) {
    const entry = this.#past.pop()
    if (!entry) return document
    this.#future.push(entry)
    return applyEditorPatchV2(document, entry.patch, 'backward')
  }

  redo(document: EditorDocumentV2) {
    const entry = this.#future.pop()
    if (!entry) return document
    this.#past.push(entry)
    return applyEditorPatchV2(document, entry.patch, 'forward')
  }
}

export function diffEditorDocumentsV2(
  before: EditorDocumentV2,
  after: EditorDocumentV2,
): EditorDocumentPatchV2 {
  const previous = new Map(before.nodes.map((node) => [node.id, node]))
  const next = new Map(after.nodes.map((node) => [node.id, node]))
  const nodes: NodeChange[] = []
  for (const id of new Set([...previous.keys(), ...next.keys()])) {
    const beforeNode = previous.get(id)
    const afterNode = next.get(id)
    if (nodeEqual(beforeNode, afterNode)) continue
    nodes.push({
      id,
      before: beforeNode ? structuredClone(beforeNode) : undefined,
      after: afterNode ? structuredClone(afterNode) : undefined,
    })
  }
  return {
    canvas:
      before.canvas.width === after.canvas.width &&
      before.canvas.height === after.canvas.height
        ? undefined
        : { before: { ...before.canvas }, after: { ...after.canvas } },
    nodes,
  }
}

export function applyEditorPatchV2(
  document: EditorDocumentV2,
  patch: EditorDocumentPatchV2,
  direction: 'forward' | 'backward',
): EditorDocumentV2 {
  const side = direction === 'forward' ? 'after' : 'before'
  const nodes = new Map(document.nodes.map((node) => [node.id, node]))
  for (const change of patch.nodes) {
    const value = change[side]
    if (value) nodes.set(change.id, structuredClone(value))
    else nodes.delete(change.id)
  }
  return {
    ...document,
    canvas: patch.canvas ? { ...patch.canvas[side] } : { ...document.canvas },
    nodes: [...nodes.values()],
  }
}

function documentsEqual(left: EditorDocumentV2, right: EditorDocumentV2) {
  if (
    left.canvas.width !== right.canvas.width ||
    left.canvas.height !== right.canvas.height ||
    left.nodes.length !== right.nodes.length
  )
    return false
  const nodes = new Map(right.nodes.map((node) => [node.id, node]))
  return left.nodes.every((node) => nodeEqual(node, nodes.get(node.id)))
}

function nodeEqual(left?: EditorNodeV2, right?: EditorNodeV2) {
  if (left === right) return true
  if (!left || !right) return false
  return JSON.stringify(left) === JSON.stringify(right)
}
