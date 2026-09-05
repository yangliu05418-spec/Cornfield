import { useVirtualizer } from '@tanstack/react-virtual'
import { useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import type { Asset } from '#/lib/api'

export function VirtualAssetGrid({
  items,
  children,
}: {
  items: Asset[]
  children: (asset: Asset) => ReactNode
}) {
  const root = useRef<HTMLDivElement>(null)
  const scroll = useRef<HTMLElement | null>(null)
  const [layout, setLayout] = useState({ width: 0, margin: 0 })
  useLayoutEffect(() => {
    const element = root.current
    if (!element) return
    scroll.current = element.closest<HTMLElement>('.library-page')
    const update = () => {
      const parent = scroll.current
      if (!parent) return
      const width = element.clientWidth
      const margin =
        element.getBoundingClientRect().top -
        parent.getBoundingClientRect().top +
        parent.scrollTop
      setLayout((previous) =>
        previous.width === width && previous.margin === margin
          ? previous
          : { width, margin },
      )
    }
    const observer = new ResizeObserver(update)
    observer.observe(element)
    if (element.parentElement) observer.observe(element.parentElement)
    update()
    return () => observer.disconnect()
  }, [])
  const columns = Math.max(1, Math.floor((layout.width + 7) / 217))
  const rowHeight =
    Math.max(1, (layout.width - (columns - 1) * 7) / columns) + 90
  const virtualizer = useVirtualizer({
    count: Math.ceil(items.length / columns),
    getScrollElement: () => scroll.current,
    estimateSize: () => rowHeight,
    scrollMargin: layout.margin,
    overscan: 3,
    getItemKey: (index) => items[index * columns]?.id ?? index,
  })
  useLayoutEffect(() => virtualizer.measure(), [rowHeight, virtualizer])
  return (
    <div
      ref={root}
      className="asset-grid-virtual"
      style={{ height: virtualizer.getTotalSize(), position: 'relative' }}
    >
      {virtualizer.getVirtualItems().map((row) => (
        <div
          key={row.key}
          data-index={row.index}
          ref={virtualizer.measureElement}
          className="asset-grid"
          style={{
            position: 'absolute',
            width: '100%',
            top: 0,
            transform: `translateY(${row.start - layout.margin}px)`,
            gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
            paddingBottom: 22,
          }}
        >
          {items
            .slice(row.index * columns, (row.index + 1) * columns)
            .map(children)}
        </div>
      ))}
    </div>
  )
}
