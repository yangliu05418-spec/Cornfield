# GPT Image 2.5 integration

Verified against OpenRouter Images endpoint discovery on 2026-09-09:

- [Sunburst capabilities](https://openrouter.ai/api/v1/images/models/openai/gpt-image-2.5-sunburst/endpoints)
- [Flare capabilities](https://openrouter.ai/api/v1/images/models/openai/gpt-image-2.5-flare/endpoints)

Both models use the existing `POST /api/v1/images` adapter. No new provider,
secret, service, or migration is required. Existing GPT Image 2 is unchanged.

| Setting | Supported values |
| --- | --- |
| Ratio (native) | 1:1, 3:2, 2:3, 4:3, 3:4, 16:9, 9:16, 21:9, auto |
| Quality | auto, low, medium, high, xhigh, max |
| Resolution | Not advertised; do not send size/resolution |
| References | Up to 16; existing owner/format/size checks apply |
| Draws | Cornfield 1–4, one output per upstream request |

The API advertises streaming, background and compression controls. This
integration retains Cornfield's existing non-streaming ingestion and does not
expose new background/compression/moderation controls. Ratio is sent as a native
field, never appended to the prompt. Frontend quality labels include 超高/最高.

## Verification

Unit tests cover 324 adapter requests (2 models × 9 ratios × 6 qualities ×
0/1/16 references), exact model IDs, absence of undeclared fields and unchanged
prompt text. Catalog tests pin the advertised capability boundaries.

Real E2E matrix: 108 text-to-image cases plus one reference-image case per model.
Run only after the release catalog is applied and API/Worker have loaded the
same revision. Use a protected password file; do not pass a password or key on
the command line. Outputs are archived in a release-specific Canary folder.

```sh
canaryctl --profile matrix \
  --models openrouter-gpt-image-2-5-sunburst,openrouter-gpt-image-2-5-flare \
  --username Intern1 --password-file /run/private/canary-password \
  --release "$RELEASE_SHA" --model-config ./config/models.yaml \
  --report /run/private/image25-matrix.json
```

Do not confuse direct upstream probes with E2E acceptance: full acceptance also
requires authenticated submission, upload/reference ownership, job completion,
persisted assets and WebP thumbnail checks. Do not retry an uncertain paid
submission. Retain failed reports and investigate before any rerun.
