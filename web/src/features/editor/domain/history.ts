import {
  cloneEditorObject,
  editorDocumentsEqual,
  editorObjectsEqual,
} from './document'
import type { EditorDocument, EditorObject } from './document'

type ObjectChange = {
  id: string
  before?: EditorObject
  after?: EditorObject
}

export type EditorDocumentPatch = {
  canvas?: {
    before: EditorDocument['canvas']
    after: EditorDocument['canvas']
  }
  objects: ObjectChange[]
  order?: { before: string[]; after: string[] }
}

type HistoryEntry = {
  patch: EditorDocumentPatch
  mergeKey?: string
}

export class EditorHistory {
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
    before: EditorDocument,
    after: EditorDocument,
    options: { mergeKey?: string } = {},
  ) {
    if (editorDocumentsEqual(before, after)) return false
    const last = this.#past.at(-1)
    const canMerge =
      options.mergeKey &&
      last?.mergeKey === options.mergeKey &&
      editorDocumentsEqual(
        applyEditorPatch(
          applyEditorPatch(before, last.patch, 'backward'),
          last.patch,
          'forward',
        ),
        before,
      )
    if (canMerge) {
      const original = applyEditorPatch(before, last.patch, 'backward')
      last.patch = diffEditorDocuments(original, after)
    } else {
      this.#past.push({
        patch: diffEditorDocuments(before, after),
        mergeKey: options.mergeKey,
      })
      if (this.#past.length > this.#limit) this.#past.shift()
    }
    this.#future = []
    return true
  }

  undo(document: EditorDocument) {
    const entry = this.#past.pop()
    if (!entry) return document
    this.#future.push(entry)
    return applyEditorPatch(document, entry.patch, 'backward')
  }

  redo(document: EditorDocument) {
    const entry = this.#future.pop()
    if (!entry) return document
    this.#past.push(entry)
    return applyEditorPatch(document, entry.patch, 'forward')
  }
}

export function diffEditorDocuments(
  before: EditorDocument,
  after: EditorDocument,
): EditorDocumentPatch {
  const beforeObjects = new Map(before.objects.map((item) => [item.id, item]))
  const afterObjects = new Map(after.objects.map((item) => [item.id, item]))
  const ids = new Set([...beforeObjects.keys(), ...afterObjects.keys()])
  const objects: ObjectChange[] = []
  for (const id of ids) {
    const previous = beforeObjects.get(id)
    const next = afterObjects.get(id)
    if (previous && next && editorObjectsEqual(previous, next)) continue
    objects.push({
      id,
      before: previous ? cloneEditorObject(previous) : undefined,
      after: next ? cloneEditorObject(next) : undefined,
    })
  }
  const beforeOrder = before.objects.map((item) => item.id)
  const afterOrder = after.objects.map((item) => item.id)
  return {
    canvas:
      before.canvas.width === after.canvas.width &&
      before.canvas.height === after.canvas.height
        ? undefined
        : { before: { ...before.canvas }, after: { ...after.canvas } },
    objects,
    order: arraysEqual(beforeOrder, afterOrder)
      ? undefined
      : { before: beforeOrder, after: afterOrder },
  }
}

export function applyEditorPatch(
  document: EditorDocument,
  patch: EditorDocumentPatch,
  direction: 'forward' | 'backward',
): EditorDocument {
  const side = direction === 'forward' ? 'after' : 'before'
  const objects = new Map(document.objects.map((item) => [item.id, item]))
  for (const change of patch.objects) {
    const value = change[side]
    if (value) objects.set(change.id, cloneEditorObject(value))
    else objects.delete(change.id)
  }
  const desiredOrder = patch.order?.[side]
  const ordered: EditorObject[] = []
  if (desiredOrder) {
    for (const id of desiredOrder) {
      const object = objects.get(id)
      if (!object) continue
      ordered.push(object)
      objects.delete(id)
    }
  } else {
    for (const object of document.objects) {
      const value = objects.get(object.id)
      if (!value) continue
      ordered.push(value)
      objects.delete(object.id)
    }
  }
  ordered.push(...objects.values())
  return {
    schema_version: 1,
    canvas: patch.canvas ? { ...patch.canvas[side] } : document.canvas,
    objects: ordered,
  }
}

function arraysEqual(a: string[], b: string[]) {
  return a.length === b.length && a.every((value, index) => value === b[index])
}
