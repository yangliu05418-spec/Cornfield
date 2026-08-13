# Editor Document V2

Cornfield V2 is a renderer-independent document protocol for the professional image editor. It is intentionally introduced before the production renderer switches from the current DOM/Canvas path.

## Compatibility boundary

| Capability                      | Save |         Current preview/export |
| ------------------------------- | ---: | -----------------------------: |
| Flat raster nodes               |  Yes | Yes, losslessly compiled to V1 |
| Stable sibling order keys       |  Yes |                            Yes |
| Groups                          |  Yes |   Pixi preview; export not yet |
| Raster alpha masks              |  Yes |   Pixi preview; export not yet |
| Group, chained or cropped masks |  Yes |                        Not yet |
| Blend modes beyond `normal`     |  Yes |                        Not yet |
| Versioned effects               |  Yes |                        Not yet |

Unsupported publishing semantics return `EDITOR_DOCUMENT_SEMANTICS_UNSUPPORTED`. The Pixi scene compiler independently supports nested group transforms, inherited visibility/opacity and one raster alpha mask per raster content node. Unsupported group, chained or cropped masks, blend modes and enabled effects are rejected before presentation; they are never silently flattened or baked into a misleading result.

## Limits

- V1 remains limited to 256 KiB and 64 objects.
- V2 is limited to 2 MiB, 500 nodes and 32 levels of parent nesting.
- Canvas dimensions remain bounded by 8192 pixels per side and 36 megapixels.
- Assets are referenced only by UUID. URLs, Base64 data and renderer-private objects are not accepted.
- Node, effect, crop, transform and blend-mode fields are allow-listed and range checked on the server.

## Migration

`MigrateV1ToV2` sorts V1 objects by `z_index`, emits flat raster nodes and assigns deterministic zero-padded order keys. `ToV1` is available only for the lossless flat-raster subset.

The Go and TypeScript implementations share `testdata/editor/v1-flat.json` and `testdata/editor/v2-flat.json` as their migration golden. A change to document semantics must update both implementations and the shared fixture in the same review.

## Lifecycle

- Project saves decode and authorize both schema versions.
- Layer decomposition and editor publishing validate renderability before creating a queued operation.
- Workers repeat the renderability check as a durable safety boundary.
- Single and bulk asset deletion detect references in both V1 `objects` and V2 `nodes`.

The renderer-neutral scene compiler traverses sibling `order_key` values deterministically, accumulates CSS-compatible affine transforms and group state, and reserves mask nodes from ordinary content drawing. The current V2 path remains test-only until the editor store and server export compiler adopt the same scene contract. Switching the live editor to V2 is a separate, reversible release step.
