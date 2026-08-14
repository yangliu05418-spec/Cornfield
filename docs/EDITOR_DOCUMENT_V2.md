# Editor Document V2

Cornfield V2 is the renderer-independent document protocol for the professional image editor. TypeScript owns interactive authoring and Pixi preview; Go owns validation and durable export. Both implementations compile the same document semantics.

## Compatibility boundary

| Capability                                      | Save | Preview/export |
| ----------------------------------------------- | ---: | -------------: |
| Flat raster nodes                               |  Yes |            Yes |
| Stable sibling order keys                       |  Yes |            Yes |
| Groups                                          |  Yes |            Yes |
| Raster alpha masks                              |  Yes |            Yes |
| Six deterministic raster blend modes            |  Yes |            Yes |
| Exposure, contrast, saturation and temperature  |  Yes |            Yes |
| Explicit raster-clipped adjustment layers       |  Yes |            Yes |
| Rectangle/ellipse vector shape masks            |  Yes |            Yes |
| Group, chained or cropped masks                 |  Yes |        Not yet |
| Group-wide or all-layers-below adjustment scope |   No |        Not yet |

Unsupported publishing semantics return `EDITOR_DOCUMENT_SEMANTICS_UNSUPPORTED`. The TypeScript preview compiler and Go export compiler both support nested group transforms, inherited visibility/opacity, one raster alpha mask per raster content node, six blend modes and the ordered color-effect pipeline. Unsupported mask structures are rejected before rendering; they are never silently flattened or baked into a misleading result.

An `adjustment` node owns no pixel asset. Its explicit `target_id` must reference an ordinary raster sibling and remains stable across reorder operations. The target's own effects run first; visible adjustment nodes then run in deterministic sibling order. Adjustment opacity is effect strength. Both compilers collapse the complete stack into one 4×5 color matrix, so any number of clipped adjustments still requires one shader filter in Pixi and one cancellable tiled pixel pass in the Worker. A raster currently used as an alpha mask cannot be an adjustment target.

A raster `shape_mask` is normalized in that raster's local pixel coordinate space, so it follows nested translation, rotation, scale and flip without rewriting its geometry. V1 supports rectangle and ellipse masks plus inversion. A raster cannot currently stack crop, independent raster alpha mask and shape mask; unsupported combinations are rejected instead of silently replacing a mask. Pixi uses vector mask geometry while the Worker inverse-transforms four subpixel samples per output pixel inside existing 512px tiles, avoiding a canvas-sized alpha allocation.

## Limits

- V1 remains limited to 256 KiB and 64 objects.
- V2 is limited to 2 MiB, 500 nodes and 32 levels of parent nesting.
- Canvas dimensions remain bounded by 8192 pixels per side and 36 megapixels.
- Assets are referenced only by UUID. Adjustment nodes are asset-free. URLs, Base64 data and renderer-private objects are not accepted.
- Node, effect, crop, transform and blend-mode fields are allow-listed and range checked on the server.

## Migration

`MigrateV1ToV2` sorts V1 objects by `z_index`, emits flat raster nodes and assigns deterministic zero-padded order keys. `ToV1` is available only for the lossless flat-raster subset.

The Go and TypeScript implementations share `testdata/editor/v1-flat.json`, `testdata/editor/v2-flat.json`, `testdata/editor/v2-group-mask.json` and `testdata/editor/color-effects-v1.json` as migration and render-semantic goldens. A change to document semantics must update both implementations and the shared fixture in the same review.

## Lifecycle

- Project saves decode and authorize both schema versions.
- Layer decomposition and editor publishing validate renderability before creating a queued operation.
- Workers repeat the renderability check as a durable safety boundary.
- Single and bulk asset deletion detect references in both V1 `objects` and V2 `nodes`.

The renderer-neutral TypeScript and Go scene compilers traverse sibling `order_key` values deterministically, accumulate CSS-compatible affine transforms and group state, reserve mask nodes from ordinary content drawing, and compile color semantics identically. The Worker exports V1 and supported V2 documents through this contract.

## Authoring commands

The renderer-independent V2 authoring kernel owns structural edits before React or Pixi integration:

- deterministic layer-tree projection and sibling order normalization;
- group/ungroup with world appearance preservation;
- attach/detach of one independent raster alpha mask;
- create and edit explicit raster-clipped adjustment layers;
- create, invert and remove local-coordinate vector shape masks;
- cycle-safe reparenting that preserves world transforms, opacity, visibility and lock state;
- node-delta undo/redo with a bounded 100-entry history.

Commands are immutable and reject edits that cannot preserve the current visual result. The production route uses the structured V2 layer tree, autosave and Pixi renderer for V2 projects. The `?document=v2` gate remains the reversible path for migrating a V1 project in memory; existing structured documents are never flattened back to V1.
