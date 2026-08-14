# Editor raster-mask protocol

Status: implementation baseline. This document defines the pixel semantics that the browser worker, Pixi preview and server export must share before the brush is exposed in the product.

## Invariants

- Source assets remain immutable. Brush and eraser gestures modify a raster-mask resource, never the source image.
- A project document stores only a mask resource identifier and immutable version. Pixel bytes and stroke samples never enter the JSON document.
- A mask is sparse 8-bit alpha in the target raster's local pixel coordinate space. `0` hides and `255` reveals.
- Tiles are 256×256 pixels; edge tiles use their natural smaller dimensions. A default tile is not stored.
- The current 8192px edge, 36MP canvas and 500-node limits remain authoritative.
- One pointer-down/up gesture is one history command. A command stores only changed rectangular regions of touched tiles.
- Browser history is bounded by both 100 commands and 64MiB of retained before/after bytes. A command larger than the budget is applied but explicitly cannot enter undo history.
- The renderer may upload only dirty tiles. It must not allocate, copy or upload a full 36MP mask after each gesture.

## Brush semantics

The canonical brush has size, hardness, opacity, spacing, mode and independent pressure influence for size and opacity. Values are validated before a stroke starts. Samples are interpolated at no more than `max(0.5px, size × spacing)` distance.

Each circular dab computes coverage at pixel centres. The hard inner radius is `radius × hardness`; the outer falloff uses smoothstep. Paint uses source-over alpha accumulation; erase multiplies the current alpha by inverse coverage. Every write is rounded to one 8-bit value, making replay deterministic.

The pure TypeScript tile engine is the first authoritative implementation. Worker and server goldens must use the same fixtures before persistence or publishing is enabled.

## Pixi preview boundary

- Every non-default mask tile is uploaded as one `r8unorm` `BufferImageSource`; updating a dirty tile replaces its byte resource and reuses the existing texture and sprite.
- Raster content is split into 256px display tiles that share the selected source texture. At the 8000×4500 limit this is 576 lightweight sprites, not a 36MP render texture.
- Only content tiles with non-default alpha receive a red-channel `MaskFilter`. Default opaque tiles have no filter, and default transparent tiles are not materialized.
- Mask sprites live in a non-rendering transform root under the content surface so offset tiles and parent transforms remain aligned. The root itself must never be used as a Pixi container mask because that invokes geometric masking instead of composing child alpha textures.
- Content sprites are individually cullable. Texture frames map document pixels onto the active display variant, so preview resolution can change without changing mask coordinates or mask bytes.
- Chromium pixel tests cover source updates, non-origin tile coordinates, default-tile release and the 36MP sprite-count gate.

## Persistence boundary

The next persistence increment will introduce project-owned mask resources and immutable versions. Each version is a manifest from tile coordinate to a derived immutable asset. Updating a mask creates a new version by committing changed tiles with an expected resource revision; it never mutates an existing version.

Publication snapshots both the editor-project revision and every referenced mask version. A later brush stroke therefore cannot alter an already queued export. Project deletion and user deletion remove manifests first and content bytes only after the existing SHA reference check proves that no version still references them.

The existing generic upload endpoint must not be used to surface mask tiles as library assets. Tile ingestion will be a project-scoped path with fixed dimensions, media type, owner checks, byte limits and orphan cleanup.
