type DraftReference = {
  key: string
  source: string
  file?: File
  previewURL?: string
}
type Draft = { references: DraftReference[] }

// File bytes are stored once per reference; typing only writes small metadata.
// All keys contain the authenticated owner. Removed references release bytes.
export async function creationDraft<T extends Draft>(
  owner: string,
  value?: T,
): Promise<T | undefined> {
  if (!owner) throw new Error('missing draft owner')
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open('cornfield-creation-drafts', 1)
    request.onupgradeneeded = () => {
      request.result.createObjectStore('drafts')
      request.result.createObjectStore('files')
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error)
  })
  try {
    return await new Promise<T | undefined>((resolve, reject) => {
      const tx = db.transaction(
        ['drafts', 'files'],
        value === undefined ? 'readonly' : 'readwrite',
      )
      const drafts = tx.objectStore('drafts')
      const files = tx.objectStore('files')
      let result: T | undefined
      if (value) {
        const live = new Set(
          value.references
            .filter((ref) => ref.source === 'local')
            .map((ref) => ref.key),
        )
        const keys = files.getAllKeys(
          IDBKeyRange.bound([owner, ''], [owner, '\uffff']),
        )
        keys.onsuccess = () => {
          for (const key of keys.result)
            if (Array.isArray(key) && !live.has(String(key[1])))
              files.delete(key)
        }
        const references = value.references.map((ref) => {
          if (ref.source !== 'local') return ref
          const { file, previewURL: _preview, ...metadata } = ref
          const key = [owner, ref.key]
          const exists = files.getKey(key)
          exists.onsuccess = () => {
            if (exists.result === undefined && file) files.put(file, key)
          }
          return metadata
        })
        drafts.put({ ...value, references }, owner)
      } else {
        const request = drafts.get(owner)
        request.onsuccess = () => {
          result = request.result as T | undefined
          if (!result) return
          for (const ref of result.references) {
            if (ref.source !== 'local') continue
            const file = files.get([owner, ref.key])
            file.onsuccess = () => {
              ref.file = file.result as File | undefined
            }
          }
        }
      }
      tx.oncomplete = () => resolve(result)
      tx.onerror = () => reject(tx.error)
      tx.onabort = () => reject(tx.error)
    })
  } finally {
    db.close()
  }
}
