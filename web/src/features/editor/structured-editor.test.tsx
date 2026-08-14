// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

import type { EditorDocumentV2 } from './domain/document-v2'
import { StructuredEditor } from './structured-editor'
import type { Asset, EditorProject } from '#/lib/api'
import type * as APIModule from '#/lib/api'
import { APIError } from '#/lib/api'

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
  cleanup()
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('StructuredEditor', () => {
  it('exposes a focused mask tool rail only for an editable raster layer', async () => {
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      if (path === '/api/v1/models')
        return Promise.resolve({ revision: 'test', models: [] })
      return Promise.resolve({})
    })
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <StructuredEditor
          project={project()}
          onBack={vi.fn()}
          onProjectChange={vi.fn()}
        />
      </QueryClientProvider>,
    )

    const brush = screen.getByRole('button', { name: '蒙版画笔' })
    const eraser = screen.getByRole('button', { name: '蒙版橡皮擦' })
    expect(brush.hasAttribute('disabled')).toBe(true)
    expect(eraser.hasAttribute('disabled')).toBe(true)
    fireEvent.click((await screen.findAllByText('人物'))[0])
    expect(brush.hasAttribute('disabled')).toBe(false)
    expect(eraser.hasAttribute('disabled')).toBe(false)
  })

  it('groups selected siblings, undoes locally, and persists the V2 document', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      if (path === '/api/v1/models')
        return Promise.resolve({ revision: 'test', models: [] })
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

  it('persists a blend mode and non-destructive adjustment', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      if (path === '/api/v1/models')
        return Promise.resolve({ revision: 'test', models: [] })
      return Promise.resolve({ revision: 2 })
    })
    render(
      <QueryClientProvider
        client={
          new QueryClient({
            defaultOptions: { queries: { retry: false } },
          })
        }
      >
        <StructuredEditor
          project={project()}
          onBack={vi.fn()}
          onProjectChange={vi.fn()}
        />
      </QueryClientProvider>,
    )

    const layerNames = await screen.findAllByText('人物')
    fireEvent.click(layerNames[0])
    fireEvent.change(screen.getByLabelText('混合模式'), {
      target: { value: 'multiply' },
    })
    fireEvent.click(screen.getByRole('checkbox', { name: '启用曝光' }))
    fireEvent.change(screen.getByRole('slider', { name: '曝光' }), {
      target: { value: '1.5' },
    })
    await vi.advanceTimersByTimeAsync(1_100)

    await waitFor(() => {
      const saveCall = apiMock.mock.calls.find(
        ([path, init]) =>
          String(path).endsWith('/document') &&
          (init as RequestInit | undefined)?.method === 'PUT',
      )
      expect(saveCall).toBeTruthy()
      const body = JSON.parse((saveCall?.[1] as RequestInit).body as string)
      expect(body.document.nodes[0]).toMatchObject({
        blend_mode: 'multiply',
        effects: [
          {
            type: 'exposure',
            enabled: true,
            parameters: { stops: 1.5 },
          },
        ],
      })
    })
  })

  it('creates and persists a clipped adjustment layer without pixel geometry', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      if (path === '/api/v1/models')
        return Promise.resolve({ revision: 'test', models: [] })
      return Promise.resolve({ revision: 2 })
    })
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <StructuredEditor
          project={project()}
          onBack={vi.fn()}
          onProjectChange={vi.fn()}
        />
      </QueryClientProvider>,
    )

    fireEvent.click((await screen.findAllByText('人物'))[0])
    fireEvent.click(screen.getByRole('button', { name: '调整层' }))
    expect(screen.getByText('人物调整')).toBeTruthy()
    expect(screen.getByText('剪贴调整图层')).toBeTruthy()
    expect(screen.getByText('强度')).toBeTruthy()
    expect(screen.queryByLabelText('混合模式')).toBeNull()

    await vi.advanceTimersByTimeAsync(1_100)
    await waitFor(() => {
      const saveCall = apiMock.mock.calls.find(
        ([path, init]) =>
          String(path).endsWith('/document') &&
          (init as RequestInit | undefined)?.method === 'PUT',
      )
      const body = JSON.parse((saveCall?.[1] as RequestInit).body as string)
      expect(body.document.nodes).toContainEqual(
        expect.objectContaining({
          type: 'adjustment',
          target_id: 'first',
          transform: [1, 0, 0, 1, 0, 0],
        }),
      )
    })
  })

  it('persists and controls a shape mask with the current design language', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    apiMock.mockImplementation((path: string) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      if (path === '/api/v1/models')
        return Promise.resolve({ revision: 'test', models: [] })
      return Promise.resolve({ revision: 2 })
    })
    const value = project()
    value.document.nodes[0].shape_mask = {
      type: 'ellipse',
      x: 0.1,
      y: 0.2,
      width: 0.6,
      height: 0.5,
      inverted: false,
    }
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <StructuredEditor
          project={value}
          onBack={vi.fn()}
          onProjectChange={vi.fn()}
        />
      </QueryClientProvider>,
    )
    fireEvent.click((await screen.findAllByText('人物'))[0])
    expect(screen.getAllByText('椭圆蒙版')).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: '反相' }))
    expect(screen.getByText('椭圆蒙版 · 已反相')).toBeTruthy()
    await vi.advanceTimersByTimeAsync(1_100)
    const saveCall = apiMock.mock.calls.find(
      ([path, init]) =>
        String(path).endsWith('/document') &&
        (init as RequestInit | undefined)?.method === 'PUT',
    )
    const body = JSON.parse((saveCall?.[1] as RequestInit).body as string)
    expect(body.document.nodes[0].shape_mask).toMatchObject({ inverted: true })
  })

  it('stops autosave after a revision conflict instead of overwriting newer work', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    apiMock.mockImplementation((path: string, init?: RequestInit) => {
      if (path === '/api/v1/assets/resolve')
        return Promise.resolve({ items: [firstAsset, secondAsset] })
      if (path === '/api/v1/models')
        return Promise.resolve({ revision: 'test', models: [] })
      if (String(path).endsWith('/document') && init?.method === 'PUT')
        return Promise.reject(
          new APIError(409, 'REVISION_CONFLICT', 'conflict'),
        )
      return Promise.resolve({})
    })
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <StructuredEditor
          project={project()}
          onBack={vi.fn()}
          onProjectChange={vi.fn()}
        />
      </QueryClientProvider>,
    )
    fireEvent.click((await screen.findAllByText('人物'))[0])
    fireEvent.change(screen.getByLabelText('混合模式'), {
      target: { value: 'multiply' },
    })
    await vi.advanceTimersByTimeAsync(1_100)
    expect(await screen.findByText('版本冲突')).toBeTruthy()
    const saveCalls = apiMock.mock.calls.filter(
      ([path, init]) =>
        String(path).endsWith('/document') &&
        (init as RequestInit | undefined)?.method === 'PUT',
    )
    expect(saveCalls).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(
      apiMock.mock.calls.filter(
        ([path, init]) =>
          String(path).endsWith('/document') &&
          (init as RequestInit | undefined)?.method === 'PUT',
      ),
    ).toHaveLength(1)
  })
})
