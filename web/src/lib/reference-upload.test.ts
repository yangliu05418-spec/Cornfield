import { describe, expect, it } from 'vitest'

import {
  normalizeReferenceMediaType,
  referenceUploadErrorMessage,
} from './reference-upload'

describe('reference upload normalization', () => {
  it.each([
    ['photo.jpg', 'image/jpeg', 'image/jpeg'],
    ['photo.JPG', 'image/jpg', 'image/jpeg'],
    ['photo.jpeg', 'image/pjpeg', 'image/jpeg'],
    ['graphic.png', 'image/x-png', 'image/png'],
    ['render.webp', 'image/webp', 'image/webp'],
    ['download.jpg', '', 'application/octet-stream'],
    ['download.png', 'application/octet-stream', 'application/octet-stream'],
  ])('normalizes %s (%s)', (name, type, expected) => {
    expect(normalizeReferenceMediaType({ name, type })).toBe(expected)
  })

  it('rejects file types outside the supported image boundary', () => {
    expect(
      normalizeReferenceMediaType({ name: 'photo.heic', type: '' }),
    ).toBeNull()
    expect(
      normalizeReferenceMediaType({ name: 'animation.gif', type: 'image/gif' }),
    ).toBeNull()
  })

  it('does not expose internal validation codes to users', () => {
    expect(referenceUploadErrorMessage('MIME_MISMATCH')).toContain('JPEG')
    expect(referenceUploadErrorMessage('SOMETHING_NEW')).not.toContain(
      'SOMETHING_NEW',
    )
  })
})
