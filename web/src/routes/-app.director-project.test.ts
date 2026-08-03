import { describe, expect, it } from 'vitest'

import { APIError } from '#/lib/api'
import { directorSaveFailureMode } from './app.director.$projectId'

describe('director autosave failure classification', () => {
  it('stops for conflicts and invalid documents', () => {
    expect(
      directorSaveFailureMode(
        new APIError(409, 'DIRECTOR_PROJECT_CONFLICT', 'conflict'),
      ),
    ).toBe('conflict')
    expect(
      directorSaveFailureMode(
        new APIError(422, 'INVALID_DIRECTOR_DOCUMENT', 'invalid'),
      ),
    ).toBe('blocked')
  })

  it('only retries transient failures', () => {
    expect(directorSaveFailureMode(new TypeError('network failed'))).toBe(
      'retry',
    )
    expect(
      directorSaveFailureMode(new APIError(429, 'RATE_LIMITED', 'busy')),
    ).toBe('retry')
    expect(
      directorSaveFailureMode(new APIError(503, 'UNAVAILABLE', 'down')),
    ).toBe('retry')
    expect(
      directorSaveFailureMode(new APIError(404, 'NOT_FOUND', 'missing')),
    ).toBe('blocked')
  })
})
