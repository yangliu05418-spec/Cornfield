// @vitest-environment jsdom
import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { JustifiedWall } from './justified-wall'
import type { WallItem } from './justified-wall'
import type { Asset } from '#/lib/api'

vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    measure() {},
    getTotalSize: () => count * 240,
    getVirtualItems: () =>
      Array.from({ length: count }, (_, index) => ({
        index,
        key: index,
        start: index * 240,
      })),
  }),
}))

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(private callback: ResizeObserverCallback) {}
      observe() {
        this.callback(
          [{ contentRect: { width: 1000 } }] as ResizeObserverEntry[],
          this as unknown as ResizeObserver,
        )
      }
      disconnect() {}
    },
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const noop = () => {}
function wall(items: WallItem[]) {
  return (
    <JustifiedWall
      items={items}
      targetHeight={240}
      onReference={noop}
      onCancel={noop}
      onDelete={noop}
      onEdit={noop}
      onDismiss={noop}
      onRetry={noop}
      onRefine={noop}
    />
  )
}
function image(id: string): WallItem {
  return {
    id,
    width: 1000,
    height: 1000,
    asset: {
      id,
      width: 1000,
      height: 1000,
      url: `/${id}.png`,
      thumb_640_url: `/${id}.webp`,
      thumb_320_url: `/${id}-320.webp`,
    } as Asset,
  }
}

describe('wall live completion without refreshing', () => {
  it.each([false, true])(
    'replaces a scrolled placeholder while unrelated new images are pending=%s',
    (withNewImage) => {
      const placeholder = {
        id: 'job:0',
        width: 1000,
        height: 1000,
        status: 'provider_pending',
      }
      const old = image('old')
      const view = render(wall([placeholder, old]))
      const scroll = view.container.querySelector('.wall-scroll')!
      scroll.scrollTop = 160
      expect(
        view.container.querySelectorAll('.placeholder-shimmer'),
      ).toHaveLength(1)

      const extra = withNewImage ? [image('unrelated')] : []
      view.rerender(wall([...extra, image('job:0'), old]))
      expect(
        view.container.querySelectorAll('.placeholder-shimmer'),
      ).toHaveLength(0)
      expect(view.container.querySelectorAll('article')).toHaveLength(2)
      expect(scroll.scrollTop).toBe(160)
      if (withNewImage) {
        fireEvent.click(view.getByRole('button', { name: '1 张新图片' }))
        expect(view.container.querySelectorAll('article')).toHaveLength(3)
      } else {
        expect(view.queryByRole('button', { name: /张新图片/ })).toBeNull()
      }
    },
  )
})
