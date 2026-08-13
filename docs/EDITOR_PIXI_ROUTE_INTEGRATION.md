# Pixi route integration

The production editor route now contains a reversible Pixi rendering path. It is intentionally opt-in through `?renderer=pixi`; DOM remains the default while device-level validation continues.

## Boundary

- Pixi renders image textures, crop masks, opacity and layer order.
- Existing DOM overlays retain selection, pointer hit testing, transform handles, crop UI and keyboard accessibility.
- Pixi is loaded as a dynamic route chunk and does not enter the create or asset-page bundles.
- The DOM image remains visible until the first Pixi scene synchronization succeeds, preventing an empty artboard on slow initialization.
- Once presented, raster `<img>` nodes are replaced by transparent geometry hit targets so the browser does not retain a duplicate decoded image.
- Entering crop mode temporarily returns content rendering to DOM because the crop workflow needs to reveal pixels outside the committed crop.

## Recovery

Initialization, texture loading and WebGL context-loss failures switch the route back to DOM without changing the document or history. The user receives a Chinese recovery notice; browser diagnostics contain no prompt, key or asset URL.

## Release gate

Before making Pixi the default:

1. Route E2E must cover opt-in mount, first-frame handoff and crop fallback.
2. The standalone gate must pass pixel comparison, 36MP/50-layer synchronization, context loss, cleanup and 640→2048→640 resource transitions.
3. Fixed-device runs must satisfy the timing budget; shared software-rendered CI continues to enforce deterministic correctness and resource gates.
4. DOM remains available for one release after default activation as the rollback path.
