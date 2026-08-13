# Cornfield Editor Pixi Renderer Spike

## Decision

PixiJS 8 is the selected retained-mode WebGL candidate for the professional image editor. It is not yet the production renderer. The existing DOM renderer remains the rollback path until the adapter is connected to the real editor route and the server-render golden suite is complete.

The spike deliberately keeps React outside the render loop: React owns panels and commands, while the renderer consumes immutable editor documents through a small adapter.

## Scope

The browser fixture exercises the current V1 semantics on a 6000×6000 (36 MP) document with 50 independently transformed 4096×4096 logical layers:

- translation, scaling and viewport movement;
- crop masks, opacity and stable z-order;
- resolution-aware 640/original texture selection;
- bounded six-way decode concurrency;
- forced WebGL context loss and restoration;
- explicit node, texture and `ImageBitmap` cleanup;
- pixel comparison against an independent Canvas2D reference for crop, rotation, flip, opacity and layer order.

The test runs in real Chromium and writes its machine-readable report to `web/output/playwright/editor-renderer-spike-report.json`.

## Acceptance gates

| Signal | Gate |
|---|---:|
| Initial 50-layer synchronization | < 2500 ms |
| Retained render call p95 | < 8 ms |
| Long tasks after warm-up | 0 |
| Significant pixel mismatch | < 1% |
| Mean absolute channel error | < 1 |
| Texture budget at spike zoom | <= 50 × 640 × 640 × 4 bytes |
| Forced context loss/restore | Both observed |
| Resources after destroy | 0 nodes, textures and estimated bytes |

The frame interval is recorded but is not a hard CI gate: headless Chromium can throttle `requestAnimationFrame` independently of renderer work. Render-call time and long tasks are the deterministic regression gates.

## Local baseline

On the development Chromium run that established the gate:

- initialization: 9 ms;
- 50-layer synchronization: 114.9 ms;
- render-call p50/p95: 0.3/0.3 ms;
- estimated texture memory: 81,920,000 bytes;
- mean absolute pixel error: 0.063;
- significant pixel mismatch: 0;
- long tasks after warm-up: 0;
- context loss and restoration: passed;
- post-destroy retained resources: 0.

CI artifacts, rather than these local numbers, are the ongoing source of truth.

## Non-goals and remaining gates

This spike does not switch the product route to Pixi and does not claim Photoshop-class coverage. Before becoming the default renderer, the next increment must add:

1. debounced resolution upgrades after zoom settles and a bounded LRU texture budget;
2. a scene compiler for the V2 node tree, groups, masks and blend modes;
3. a shared browser/Pixi/pure-Go golden fixture and tolerances;
4. overlay hit testing, selection handles and tool integration;
5. explicit safe recovery UI for unsupported WebGL and restoration failure;
6. low/medium/high device profiles and repeated open/close memory tests.

The spike is kept as a permanent CI regression test so dependency or browser upgrades cannot silently change transform semantics or resource behavior.
