// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

import type { EditorDocumentV2 } from './domain/document-v2'
import { StructuredEditor } from './structured-editor'
import type { Asset, EditorProject } from '#/lib/api'
import type * as APIModule from '#/lib/api'

const apiMock = vi.fn()

vi.mock('#/lib/api', async (importOriginal) => {
  const original = await importOriginal<typeof APIModule>()
  return { ...original, api: (...args: unknown[]) => apiMock(...args) }
})

vi.mock('#/components/app-shell', () => ({
  AppShell: ({ children }: { children: ReactNode }) => children,
}))

vi.mock('./renderer/pixi-surface', () => ({
  PixiSurface: ({
    onPresentedChange,
  }: {
    onPresentedChange: (value: boolean) => void
  }) => {
    queueMicrotask(() => onPresentedChange(true))
    return <div data-testid="pixi-surface" />
  },
}))

const firstAsset: Asset = {
  id: '00000000-0000-4000-8000-000000000001',
  kind: 'upload',
  media_type: 'image/png',
  width: 100,
  height: 100,
  byte_size: 100,
  sha256: 'first',
  url: '/first',
  thumb_320_url: '/first-320',
  thumb_640_url: '/first-640',
  thumb_1280_url: '/first-1280',
  created_at: '2026-08-14T00:00:00Z',
}
const secondAsset = {
  ...firstAsset,
  id: '00000000-0000-4000-8000-000000000002',
  sha256: 'second',
  url: '/second',
}

function project(): EditorProject & { document: EditorDocumentV2 } {
  return {
    id: '00000000-0000-4000-8000-000000000010',
    source_asset_id: firstAsset.id,
    name: '图层测试',
    revision: 1,
    created_at: '2026-08-14T00:00:00Z',
    updated_at: '2026-08-14T00:00:00Z',
    document: {
      schema_version: 2,
      renderer_semantics_version: 1,
      canvas: { width: 100, height: 100 },
      nodes: [
        {
          id: 'first',
          type: 'raster',
          name: '人物',
          parent_id: null,
          order_key: '00000000',
          transform: [1, 0, 0, 1, 0, 0],
          opacity: 1,
          blend_mode: 'normal',
          visible: true,
          locked: false,
          asset_id: firstAsset.id,
        },
        {
          id: 'second',
          type: 'raster',
          name: '背景',
          parent_id: null,
          order_key: '00000001',
          transform: [1, 0, 0, 1, 0, 0],
          opacity: 1,
          blend_mode: 'normal',
          visible: true,
          locked: false,
          asset_id: secondAsset.id,
        },
      ],
    },
  }
}

afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('StructuredEditor', () => {
  it('groups selected siblings, undoes locally, and persists the V2 document', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      return Promise.resolve({ revision: 2 })
    })
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })

    render(
      <QueryClientProvider client={queryClient}>
        <StructuredEditor
          project={project()}
          onBack={vi.fn()}
          onProjectChange={vi.fn()}
        />
      </QueryClientProvider>,
    )

    fireEvent.click(await screen.findByText('背景'))
    fireEvent.click(screen.getByText('人物'), { shiftKey: true })
    fireEvent.click(screen.getByRole('button', { name: '成组' }))

    expect(screen.getByText('新建组')).toBeTruthy()
    expect(
      screen.getByRole('button', { name: '撤销' }).hasAttribute('disabled'),
    ).toBe(false)

    await vi.advanceTimersByTimeAsync(1_100)
    await waitFor(() =>
      expect(apiMock).toHaveBeenCalledWith(
        expect.stringContaining('/document'),
        expect.objectContaining({ method: 'PUT' }),
      ),
    )

    fireEvent.click(screen.getByRole('button', { name: '撤销' }))
    expect(screen.queryByText('新建组')).toBeNull()
  })
})
