# Editor Document V2

Cornfield V2 is a renderer-independent document protocol for the professional image editor. It is intentionally introduced before the production renderer switches from the current DOM/Canvas path.

## Compatibility boundary

| Capability                      | Save |         Current preview/export |
| ------------------------------- | ---: | -----------------------------: |
| Flat raster nodes               |  Yes | Yes, losslessly compiled to V1 |
| Stable sibling order keys       |  Yes |                            Yes |
| Groups                          |  Yes |            Pixi preview/export |
| Raster alpha masks              |  Yes |            Pixi preview/export |
| Group, chained or cropped masks |  Yes |                        Not yet |
| Blend modes beyond `normal`     |  Yes |                        Not yet |
| Versioned effects               |  Yes |                        Not yet |

Unsupported publishing semantics return `EDITOR_DOCUMENT_SEMANTICS_UNSUPPORTED`. The TypeScript preview compiler and Go export compiler both support nested group transforms, inherited visibility/opacity and one raster alpha mask per raster content node. Unsupported group, chained or cropped masks, blend modes and enabled effects are rejected before rendering; they are never silently flattened or baked into a misleading result.

## Limits

- V1 remains limited to 256 KiB and 64 objects.
- V2 is limited to 2 MiB, 500 nodes and 32 levels of parent nesting.
- Canvas dimensions remain bounded by 8192 pixels per side and 36 megapixels.
- Assets are referenced only by UUID. URLs, Base64 data and renderer-private objects are not accepted.
- Node, effect, crop, transform and blend-mode fields are allow-listed and range checked on the server.

## Migration

`MigrateV1ToV2` sorts V1 objects by `z_index`, emits flat raster nodes and assigns deterministic zero-padded order keys. `ToV1` is available only for the lossless flat-raster subset.

The Go and TypeScript implementations share `testdata/editor/v1-flat.json`, `testdata/editor/v2-flat.json` and `testdata/editor/v2-group-mask.json` as migration and render-scene goldens. A change to document semantics must update both implementations and the shared fixture in the same review.

## Lifecycle

- Project saves decode and authorize both schema versions.
- Layer decomposition and editor publishing validate renderability before creating a queued operation.
- Workers repeat the renderability check as a durable safety boundary.
- Single and bulk asset deletion detect references in both V1 `objects` and V2 `nodes`.

The renderer-neutral TypeScript and Go scene compilers traverse sibling `order_key` values deterministically, accumulate CSS-compatible affine transforms and group state, and reserve mask nodes from ordinary content drawing. The Worker exports V1 and supported V2 documents through this contract. The live editor store still writes V1 documents; switching authoring to V2 remains a separate, reversible release step.

## Authoring commands

The renderer-independent V2 authoring kernel owns structural edits before React or Pixi integration:

- deterministic layer-tree projection and sibling order normalization;
- group/ungroup with world appearance preservation;
- attach/detach of one independent raster alpha mask;
- cycle-safe reparenting that preserves world transforms, opacity, visibility and lock state;
- node-delta undo/redo with a bounded 100-entry history.

Commands are immutable and reject edits that cannot preserve the current visual result. The production editor route remains on V1 until its selection, autosave and layer-panel state are migrated together.

The real editor route has a reversible flat-authoring development gate at `?document=v2`. It migrates a V1 project in memory, keeps V2 as the autosave and JSON-download source of truth, and projects only the lossless flat-raster subset into the existing interaction code. Existing structured V2 documents are never flattened; until the V2 layer panel takes ownership, the route displays a protected handoff state instead of editing or overwriting them.
