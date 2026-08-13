import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'

import {
  applyEditorColorMatrixV1,
  compileEditorColorMatrixV1,
} from './color-effects'

describe('editor color effects V1', () => {
  it('matches the shared cross-language fixture', () => {
    const fixture = JSON.parse(
      readFileSync(
        new URL(
          '../../../../../testdata/editor/color-effects-v1.json',
          import.meta.url,
        ),
        'utf8',
      ),
    ) as {
      effects: Parameters<typeof compileEditorColorMatrixV1>[0]
      input: [number, number, number, number]
      output: [number, number, number, number]
    }
    const output = applyEditorColorMatrixV1(
      compileEditorColorMatrixV1(fixture.effects),
      fixture.input,
    )
    output.forEach((channel, index) =>
      expect(channel).toBeCloseTo(fixture.output[index], 11),
    )
  })

  it('applies enabled effects in document order', () => {
    const matrix = compileEditorColorMatrixV1([
      { type: 'exposure', version: 1, enabled: true, parameters: { stops: 1 } },
      {
        type: 'contrast',
        version: 1,
        enabled: true,
        parameters: { amount: 1 },
      },
      {
        type: 'saturation',
        version: 1,
        enabled: false,
        parameters: { amount: -1 },
      },
    ])
    const channels = applyEditorColorMatrixV1(matrix, [0.2, 0.3, 0.4, 0.75])
    expect(channels[0]).toBeCloseTo(0.3, 10)
    expect(channels[1]).toBeCloseTo(0.7, 10)
    expect(channels[2]).toBe(1)
    expect(channels[3]).toBe(0.75)
  })

  it('matches the Go temperature and desaturation fixture', () => {
    const matrix = compileEditorColorMatrixV1([
      {
        type: 'temperature',
        version: 1,
        enabled: true,
        parameters: { kelvin_delta: 5000 },
      },
      {
        type: 'saturation',
        version: 1,
        enabled: true,
        parameters: { amount: -1 },
      },
    ])
    const channels = applyEditorColorMatrixV1(matrix, [0.8, 0.4, 0.2, 1])
    const luma = 0.2126 * 0.8 * 1.1 + 0.7152 * 0.4 * 1.025 + 0.0722 * 0.2 * 0.9
    expect(channels[0]).toBeCloseTo(luma, 10)
    expect(channels[1]).toBeCloseTo(luma, 10)
    expect(channels[2]).toBeCloseTo(luma, 10)
    expect(channels[3]).toBe(1)
  })
})
