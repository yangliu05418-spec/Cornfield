# Editor raster-mask protocol

Status: implemented. This document defines the pixel semantics shared by the browser worker, Pixi preview, immutable persistence and server export.

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

- `editor_raster_masks` owns one mask per project raster node and binds it to the immutable source asset dimensions.
- `editor_raster_mask_versions` is append-only. A commit may branch from any existing version after an undo; the project revision lock still serializes the authoritative document update.
- A version is a complete sparse manifest. Unchanged rows are copied from its base version, while immutable `.a8` bytes remain content-addressed and deduplicated by SHA-256.
- The project document stores only `{resource_id, version}`. Saving a document validates owner, project, target node, source asset and version before accepting the reference.
- Tile commits use a project-scoped multipart endpoint with a JSON manifest first, at most 576 full Alpha8 tile parts and a 40 MiB body limit. Default tiles are represented by `delete`, never by stored bytes.
- The API records a short database-visible blob write lease before committing bytes. Worker orphan cleanup includes active leases and every immutable mask-version reference, closing the cross-process put/delete race.
- Publication snapshots both the editor-project revision and every referenced mask version. The Worker loads only materialized tiles and samples them directly into its existing 512px render tiles; it never creates a full-canvas Alpha image.
- Project and user deletion remove manifests through database cascades. The conservative content sweeper deletes bytes only after the SHA reference recheck proves that no version or active write lease still references them.

The generic upload endpoint is not used for mask tiles, and mask bytes never appear as user-library assets.

## Product interaction and save coordination

- The editor exposes selection, mask brush and mask eraser in the existing Cornfield tool rail. Brush size, hardness, flow and pressure remain contextual canvas controls rather than permanent inspector noise.
- Entering a mask tool flushes the ordinary project document first. Creating the first mask atomically attaches version `0` to the selected raster and advances the project revision.
- Existing immutable manifests load with bounded concurrency before Pixi presents the masked node. A missing manifest never falls back to showing the unmasked source.
- Pointer samples are coalesced into one Worker gesture. Preview mutations update only dirty tiles; pointer-up stages the gesture's complete dirty-tile set for persistence.
- Mask persistence is debounced and serialized. A successful commit advances both the immutable mask version and project revision; later strokes remain queued against the new version.
- Selecting another tool or leaving the workspace waits for pending mask and document saves. Revision conflicts and invalid manifests stop autosave with a Chinese actionable message instead of retrying indefinitely.
- Mask undo/redo is session-local and uses the bounded tile history. Reloading restores the latest saved pixels but intentionally starts a fresh local history.
