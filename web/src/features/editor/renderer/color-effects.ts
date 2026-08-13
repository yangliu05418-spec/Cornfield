import type { EditorEffectV2 } from '../domain/document-v2'

export type EditorColorMatrixV1 = [
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
  number,
]

export function identityEditorColorMatrixV1(): EditorColorMatrixV1 {
  return [1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 1, 0]
}

export function isIdentityEditorColorMatrixV1(matrix: EditorColorMatrixV1) {
  const identity = identityEditorColorMatrixV1()
  return matrix.every((value, index) => value === identity[index])
}

export function compileEditorColorMatrixV1(
  effects: readonly EditorEffectV2[],
): EditorColorMatrixV1 {
  let result = identityEditorColorMatrixV1()
  for (const effect of effects) {
    if (!effect.enabled) continue
    result = multiplyEditorColorMatricesV1(effectMatrix(effect), result)
  }
  return result
}

export function compileEditorColorMatrixWithStrengthV1(
  effects: readonly EditorEffectV2[],
  strength: number,
) {
  return interpolateEditorColorMatrixV1(
    identityEditorColorMatrixV1(),
    compileEditorColorMatrixV1(effects),
    strength,
  )
}

export function composeEditorColorMatricesV1(
  matrices: readonly EditorColorMatrixV1[],
) {
  let result = identityEditorColorMatrixV1()
  for (const matrix of matrices)
    result = multiplyEditorColorMatricesV1(matrix, result)
  return result
}

export function interpolateEditorColorMatrixV1(
  from: EditorColorMatrixV1,
  to: EditorColorMatrixV1,
  amount: number,
) {
  const strength = clamp(amount)
  return from.map(
    (value, index) => value + (to[index] - value) * strength,
  ) as EditorColorMatrixV1
}

export function applyEditorColorMatrixV1(
  matrix: EditorColorMatrixV1,
  channels: readonly [number, number, number, number],
): [number, number, number, number] {
  const [red, green, blue, alpha] = channels
  return [
    clamp(
      matrix[0] * red +
        matrix[1] * green +
        matrix[2] * blue +
        matrix[3] * alpha +
        matrix[4],
    ),
    clamp(
      matrix[5] * red +
        matrix[6] * green +
        matrix[7] * blue +
        matrix[8] * alpha +
        matrix[9],
    ),
    clamp(
      matrix[10] * red +
        matrix[11] * green +
        matrix[12] * blue +
        matrix[13] * alpha +
        matrix[14],
    ),
    clamp(
      matrix[15] * red +
        matrix[16] * green +
        matrix[17] * blue +
        matrix[18] * alpha +
        matrix[19],
    ),
  ]
}

function effectMatrix(effect: EditorEffectV2): EditorColorMatrixV1 {
  const matrix = identityEditorColorMatrixV1()
  if (effect.type === 'exposure') {
    const gain = 2 ** effect.parameters.stops
    matrix[0] = gain
    matrix[6] = gain
    matrix[12] = gain
  } else if (effect.type === 'contrast') {
    const gain = 2 ** effect.parameters.amount
    const offset = (1 - gain) / 2
    matrix[0] = gain
    matrix[6] = gain
    matrix[12] = gain
    matrix[4] = offset
    matrix[9] = offset
    matrix[14] = offset
  } else if (effect.type === 'saturation') {
    const gain = 1 + effect.parameters.amount
    const inverse = 1 - gain
    const [redLuma, greenLuma, blueLuma] = [0.2126, 0.7152, 0.0722]
    matrix[0] = redLuma * inverse + gain
    matrix[1] = greenLuma * inverse
    matrix[2] = blueLuma * inverse
    matrix[5] = redLuma * inverse
    matrix[6] = greenLuma * inverse + gain
    matrix[7] = blueLuma * inverse
    matrix[10] = redLuma * inverse
    matrix[11] = greenLuma * inverse
    matrix[12] = blueLuma * inverse + gain
  } else {
    const warmth = effect.parameters.kelvin_delta / 10_000
    matrix[0] = 1 + 0.2 * warmth
    matrix[6] = 1 + 0.05 * warmth
    matrix[12] = 1 - 0.2 * warmth
  }
  return matrix
}

function multiplyEditorColorMatricesV1(
  left: EditorColorMatrixV1,
  right: EditorColorMatrixV1,
): EditorColorMatrixV1 {
  const result = Array<number>(20).fill(0) as EditorColorMatrixV1
  for (let row = 0; row < 4; row += 1) {
    for (let column = 0; column < 4; column += 1) {
      for (let inner = 0; inner < 4; inner += 1)
        result[row * 5 + column] +=
          left[row * 5 + inner] * right[inner * 5 + column]
    }
    result[row * 5 + 4] = left[row * 5 + 4]
    for (let inner = 0; inner < 4; inner += 1)
      result[row * 5 + 4] += left[row * 5 + inner] * right[inner * 5 + 4]
  }
  return result
}

function clamp(value: number) {
  return Math.max(0, Math.min(1, value))
}
