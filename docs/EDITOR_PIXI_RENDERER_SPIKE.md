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
- a reference-counted LRU texture cache with a 256 MiB default budget;
- deterministic asset-level variant planning and 150 ms settled-zoom upgrades;
- pixel comparison against an independent Canvas2D reference for crop, rotation, flip, opacity and layer order.
- a second V2 pixel golden covering nested group transforms, inherited opacity and an independently transformed raster alpha mask.

The test runs in real Chromium and writes its machine-readable report to `web/output/playwright/editor-renderer-spike-report.json`.

## Acceptance gates

| Signal                           |                                  Gate |
| -------------------------------- | ------------------------------------: |
| Initial 50-layer synchronization |                             < 2500 ms |
| Retained render call p95         |                                < 8 ms |
| Significant pixel mismatch       |                                  < 1% |
| Mean absolute channel error      |                                   < 1 |
| Texture budget at spike zoom     |           <= 50 × 640 × 640 × 4 bytes |
| Settled zoom variant transition  |         640 → required high-res → 640 |
| Forced context loss/restore      |                         Both observed |
| Resources after destroy          | 0 nodes, textures and estimated bytes |

Frame intervals and long tasks are always recorded but are not hard gates on shared GitHub runners: headless Chromium can use software WebGL and throttle `requestAnimationFrame` independently of renderer work. Pixel semantics, CPU submission time, texture budget, context recovery and cleanup remain deterministic CI gates.

Fixed-device performance runs set `EDITOR_SPIKE_ENFORCE_DEVICE_TIMING=1`; those runs additionally require frame-interval p95 below 22.3 ms (45 fps) and zero long tasks after warm-up. Device class and browser/GPU details must accompany the report.

Use the dedicated command so the timing gate cannot be accidentally omitted. The run fails when the profile label is absent or Chromium reports a software renderer:

```powershell
$env:EDITOR_DEVICE_PROFILE = 'windows-mid-2026'
$env:EDITOR_DEVICE_CHANNEL = 'chrome'
$env:EDITOR_DEVICE_HEADLESS = '0'
pnpm spike:editor:device
```

The JSON artifact records the profile label, capture time, browser, OS platform, logical processors, memory hint, DPR, viewport, screen and unmasked GPU vendor/renderer. Profile labels describe a stable lab device class and must not contain a person's name or other identifying information.

## Local baseline

On the development Chromium run that established the gate:

- initialization: 9 ms;
- 50-layer synchronization: 114.9 ms;
- render-call p50/p95: 0.3/0.3 ms;
- estimated texture memory: 81,920,000 bytes;
- zoom transition active bytes: 1,638,400 → 16,777,216 → 1,638,400;
- mean absolute pixel error: 0.063;
- significant pixel mismatch: 0;
- long tasks after warm-up: 0;
- context loss and restoration: passed;
- post-destroy retained resources: 0.

CI artifacts, rather than these local numbers, are the ongoing source of truth.

## Non-goals and remaining gates

This spike does not switch the product route to Pixi and does not claim Photoshop-class coverage. Before becoming the default renderer, the next increment must add:

1. a scene compiler for the V2 node tree, groups, masks and blend modes;
2. a shared browser/Pixi/pure-Go golden fixture and tolerances;
3. overlay hit testing, selection handles and tool integration;
4. explicit safe recovery UI for unsupported WebGL and restoration failure;
5. low/medium/high device profiles and repeated open/close memory tests;
6. tile-pyramid loading before raising the current 36 MP production boundary.

The spike is kept as a permanent CI regression test so dependency or browser upgrades cannot silently change transform semantics or resource behavior.
