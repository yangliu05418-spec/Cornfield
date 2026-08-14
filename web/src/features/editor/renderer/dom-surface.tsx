import type { CSSProperties } from 'react'

import type { Asset } from '#/lib/api'
import type { EditorDocumentV3 } from '../domain/document-v3'
import { compileEditorRenderScene } from './scene-compiler'
import type { EditorViewport } from './types'

type Props = {
  document: EditorDocumentV3
  assets: ReadonlyMap<string, Asset>
  viewport: EditorViewport
}

export function EditorDOMSurface({ document, assets, viewport }: Props) {
  const scene = compileEditorRenderScene(document)
  const scale = viewport.zoom / 100
  const nodesByArtboard = new Map<string, typeof scene.nodes>()
  for (const node of scene.nodes) {
    if (!node.artboardID || node.role === 'mask') continue
    const nodes = nodesByArtboard.get(node.artboardID) ?? []
    nodes.push(node)
    nodesByArtboard.set(node.artboardID, nodes)
  }

  return (
    <div
      className="editor-dom-surface"
      data-testid="editor-dom-surface"
      aria-hidden="true"
    >
      {scene.artboards?.map((artboard) => (
        <div
          key={artboard.id}
          className="editor-dom-artboard"
          style={{
            width: artboard.width,
            height: artboard.height,
            visibility: artboard.visible ? 'visible' : 'hidden',
            transform: `translate(${viewport.panX}px, ${viewport.panY}px) scale(${scale}) translate(${artboard.x}px, ${artboard.y}px)`,
          }}
        >
          {(nodesByArtboard.get(artboard.id) ?? []).map((node) => {
            const asset = assets.get(node.assetID)
            if (!asset || !node.visible) return null
            const crop = node.crop
            const clipPath = crop
              ? `inset(${crop.y * 100}% ${(1 - crop.x - crop.width) * 100}% ${(1 - crop.y - crop.height) * 100}% ${crop.x * 100}%)`
              : node.shapeMask?.type === 'ellipse'
                ? 'ellipse(50% 50% at 50% 50%)'
                : undefined
            return (
              <img
                key={node.id}
                src={asset.thumb_1280_url || asset.thumb_640_url || asset.url}
                alt=""
                draggable={false}
                style={
                  {
                    width: asset.width,
                    height: asset.height,
                    opacity: node.opacity,
                    transform: `matrix(${node.transform.join(',')})`,
                    mixBlendMode:
                      node.blendMode === 'normal' ? 'normal' : node.blendMode,
                    clipPath,
                    '--editor-node-order': node.order,
                  } as CSSProperties
                }
              />
            )
          })}
        </div>
      ))}
    </div>
  )
}
