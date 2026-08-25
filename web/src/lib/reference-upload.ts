const referenceMediaAliases: Record<string, string> = {
  'image/jpeg': 'image/jpeg',
  'image/jpg': 'image/jpeg',
  'image/pjpeg': 'image/jpeg',
  'image/png': 'image/png',
  'image/x-png': 'image/png',
  'image/webp': 'image/webp',
}

const referenceExtensions: Record<string, string> = {
  '.jpeg': 'image/jpeg',
  '.jpg': 'image/jpeg',
  '.png': 'image/png',
  '.webp': 'image/webp',
}

export function normalizeReferenceMediaType(file: Pick<File, 'name' | 'type'>) {
  const declared = file.type.split(';', 1)[0].trim().toLowerCase()
  const normalized = referenceMediaAliases[declared]
  if (normalized) return normalized

  const dot = file.name.lastIndexOf('.')
  const extension = dot >= 0 ? file.name.slice(dot).toLowerCase() : ''
  if (referenceExtensions[extension]) {
    // The server sniffs and fully decodes the bytes. An unknown browser MIME
    // must not prevent a valid JPEG/PNG/WebP file from reaching that check.
    return 'application/octet-stream'
  }
  return null
}

export function clipboardImageFiles(data: DataTransfer): File[] {
  const files: File[] = []
  for (const item of Array.from(data.items)) {
    if (item.kind !== 'file' || !item.type.toLowerCase().startsWith('image/'))
      continue
    const file = item.getAsFile()
    if (file && normalizeReferenceMediaType(file)) files.push(file)
  }
  if (files.length) return files
  return Array.from(data.files).filter(
    (file) => normalizeReferenceMediaType(file) !== null,
  )
}

export function referenceUploadErrorMessage(code?: string) {
  switch (code) {
    case 'MIME_MISMATCH':
    case 'IMAGE_INVALID':
    case 'IMAGE_DECODE_FAILED':
      return '图片无法识别，请确认文件是完整的 JPEG、PNG 或 WebP'
    case 'UPLOAD_VARIANT_FAILED':
      return '图片预览处理失败，请稍后重试'
    case 'OWNER_UNAVAILABLE':
      return '当前账户暂时无法上传图片'
    default:
      return '参考图验证失败，请更换图片或稍后重试'
  }
}
