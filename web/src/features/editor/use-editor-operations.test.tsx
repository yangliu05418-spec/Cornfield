// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

import { useEditorOperations } from './use-editor-operations'
import type { Asset, LayerSet } from '#/lib/api'
import type * as APIModule from '#/lib/api'

const apiMock = vi.fn()

vi.mock('#/lib/api', async (importOriginal) => {
  const original = await importOriginal<typeof APIModule>()
  return { ...original, api: (...args: unknown[]) => apiMock(...args) }
})

const asset: Asset = {
  id: '00000000-0000-4000-8000-000000000001',
  kind: 'derived',
  media_type: 'image/png',
  width: 100,
  height: 100,
  byte_size: 100,
  sha256: 'asset',
  url: '/asset',
  thumb_320_url: '/asset-320',
  thumb_640_url: '/asset-640',
  thumb_1280_url: '/asset-1280',
  created_at: '2026-08-14T00:00:00Z',
}

const layerSet: LayerSet = {
  id: '00000000-0000-4000-8000-000000000002',
  source_revision: 1,
  base_asset: asset,
  items: [],
  package_ready: false,
  applied_to_project: true,
}

class FakeEventSource {
  addEventListener() {}
  removeEventListener() {}
  close() {}
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('useEditorOperations', () => {
  it('flushes saves before submitting and applies a terminal layer result once', async () => {
    vi.stubGlobal('EventSource', FakeEventSource)
    const flushSaves = vi.fn().mockResolvedValue(undefined)
    const onLayerSetReady = vi.fn()
    apiMock.mockImplementation((path: string, init?: RequestInit) => {
      if (path === '/api/v1/models')
        return Promise.resolve({
          revision: 'test',
          models: [
            {
              id: 'byteplus-seedream-5-0-pro',
              capabilities: { layer_decomposition: true },
              availability: { can_submit: true },
            },
          ],
        })
      if (path.endsWith('/layer-decompositions') && init?.method === 'POST')
        return Promise.resolve({
          id: 'operation',
          estimated_wait: {
            lower_seconds: 70,
            upper_seconds: 90,
            sample_size: 10,
            basis: 'global',
          },
        })
      if (path === '/api/v1/asset-operations/operation')
        return Promise.resolve({
          id: 'operation',
          editor_project_id: 'project',
          operation_type: 'layer_decomposition',
          status: 'succeeded',
          source_revision: 1,
          submission_uncertain: false,
          layer_set: layerSet,
          created_at: '2026-08-14T00:00:00Z',
          updated_at: '2026-08-14T00:00:01Z',
        })
      throw new Error(`unexpected request: ${path}`)
    })
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(
      () =>
        useEditorOperations({
          projectID: 'project',
          getRevision: () => 1,
          getActiveArtboardID: () => 'artboard-1',
          flushSaves,
          onLayerSetReady,
          onNotice: vi.fn(),
        }),
      { wrapper },
    )

    await waitFor(() => expect(result.current.canDecompose).toBe(true))
    await act(() =>
      result.current.startDecomposition({
        prompt: '',
        resolution: 'auto',
        mode: 'standard',
      }),
    )

    expect(flushSaves).toHaveBeenCalledTimes(1)
    const submit = apiMock.mock.calls.find(([path]) =>
      String(path).endsWith('/layer-decompositions'),
    )
    expect(
      JSON.parse((submit?.[1] as RequestInit).body as string),
    ).toMatchObject({ artboard_id: 'artboard-1' })
    expect(result.current.estimatedWait).toEqual({
      lower_seconds: 70,
      upper_seconds: 90,
      sample_size: 10,
      basis: 'global',
    })
    await waitFor(() => expect(onLayerSetReady).toHaveBeenCalledTimes(1))
    expect(onLayerSetReady).toHaveBeenCalledWith(layerSet, 1)
    await queryClient.invalidateQueries({
      queryKey: ['asset-operation', 'operation'],
    })
    expect(onLayerSetReady).toHaveBeenCalledTimes(1)
  })
})
