import { describe, expect, it } from 'vitest'

import { applyPromptRefinements } from './prompt-refiner-dialog'
import type { PromptRefineResponse } from '#/lib/api'

const result: PromptRefineResponse = {
  policy_version: 'test',
  status: 'findings',
  segments: [
    { text: 'A ' },
    { text: 'bloody', finding_id: 'blood' },
    { text: ' field and ' },
    { text: '乳头', finding_id: 'manual' },
  ],
  findings: [],
  diagnostics: [],
}

describe('prompt refinement application', () => {
  it('only replaces selected mapped findings', () => {
    const applied = applyPromptRefinements(
      result,
      new Set(['blood']),
      { blood: 'crimson' },
      { start: 4, end: 4 },
    )
    expect(applied.prompt).toBe('A crimson field and 乳头')
    expect(applied.selection).toEqual({ start: 9, end: 9 })
  })

  it('preserves unselected and manual-only segments', () => {
    const applied = applyPromptRefinements(
      result,
      new Set(),
      {},
      { start: 20, end: 20 },
    )
    expect(applied.prompt).toBe('A bloody field and 乳头')
    expect(applied.selection).toEqual({ start: 20, end: 20 })
  })
})
