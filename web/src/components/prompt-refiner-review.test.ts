import { describe, expect, it } from 'vitest'

import { mapRefinedSelection, promptChangeParts } from './prompt-refiner-review'

describe('prompt refiner review', () => {
  it('maps selections around the changed span', () => {
    const before = 'A bloody field at dusk'
    const after = 'A dramatic red field at dusk'

    expect(mapRefinedSelection(before, after, { start: 1, end: 1 })).toEqual({
      start: 1,
      end: 1,
    })
    expect(mapRefinedSelection(before, after, { start: 5, end: 5 })).toEqual({
      start: 14,
      end: 14,
    })
    expect(mapRefinedSelection(before, after, { start: 22, end: 22 })).toEqual({
      start: 28,
      end: 28,
    })
  })

  it('builds one stable, readable change span', () => {
    expect(
      promptChangeParts(
        'blood over a quiet cornfield',
        'crimson accents over a quiet cornfield',
      ),
    ).toEqual({
      before: ['', 'blood', ' over a quiet cornfield'],
      after: ['', 'crimson accents', ' over a quiet cornfield'],
    })
  })
})
