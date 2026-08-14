// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { EditorOperationWaiting } from './editor-operation-waiting'

describe('EditorOperationWaiting', () => {
  const estimate = {
    lower_seconds: 70,
    upper_seconds: 90,
    sample_size: 10,
    basis: 'global',
  }

  it('shows the immutable wait range and real elapsed time', () => {
    render(
      <EditorOperationWaiting
        status="provider_processing"
        elapsed={42}
        estimate={estimate}
      />,
    )

    expect(screen.getByText('整理图层关系')).toBeTruthy()
    expect(screen.getByText('预计 70–90 秒，已等待 42 秒')).toBeTruthy()
    expect(screen.queryByText(/%/)).toBeNull()
  })

  it('switches to an honest overdue message after the upper bound', () => {
    render(
      <EditorOperationWaiting
        status="provider_processing"
        elapsed={91}
        estimate={estimate}
      />,
    )

    expect(screen.getByText('比预计稍久，任务仍在后台处理')).toBeTruthy()
  })
})
