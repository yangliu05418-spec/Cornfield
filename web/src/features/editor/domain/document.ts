export type EditorTransform = [number, number, number, number, number, number]

export type EditorCrop = {
  x: number
  y: number
  width: number
  height: number
}

export type EditorObject = {
  id: string
  name?: string
  asset_id: string
  transform: EditorTransform
  opacity: number
  visible: boolean
  locked: boolean
  z_index: number
  crop?: EditorCrop
}

export type EditorDocumentV1 = {
  schema_version: 1
  canvas: { width: number; height: number }
  objects: EditorObject[]
}

// Keep the public name stable while schema V1 remains the persisted format.
// V2 will be introduced as a separate discriminated union and migrated at the
// API boundary instead of leaking renderer-specific objects into this type.
export type EditorDocument = EditorDocumentV1

export function editorObjectsEqual(a: EditorObject, b: EditorObject) {
  return (
    a === b ||
    (a.id === b.id &&
      a.name === b.name &&
      a.asset_id === b.asset_id &&
      a.opacity === b.opacity &&
      a.visible === b.visible &&
      a.locked === b.locked &&
      a.z_index === b.z_index &&
      a.transform.every((value, index) => value === b.transform[index]) &&
      cropsEqual(a.crop, b.crop))
  )
}

export function editorDocumentsEqual(
  a: EditorDocument | null,
  b: EditorDocument | null,
) {
  if (a === b) return true
  if (!a || !b) return false
  if (
    a.canvas.width !== b.canvas.width ||
    a.canvas.height !== b.canvas.height ||
    a.objects.length !== b.objects.length
  )
    return false
  return a.objects.every((object, index) =>
    editorObjectsEqual(object, b.objects[index]),
  )
}

export function cloneEditorObject(object: EditorObject): EditorObject {
  return {
    ...object,
    transform: [...object.transform],
    crop: object.crop ? { ...object.crop } : undefined,
  }
}

function cropsEqual(a?: EditorCrop, b?: EditorCrop) {
  if (a === b) return true
  if (!a || !b) return false
  return (
    a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height
  )
}
