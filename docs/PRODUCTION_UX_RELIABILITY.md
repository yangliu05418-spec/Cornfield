# Production UX reliability update

## Scope

This update follows the September 5 production audit. It keeps the current
deployment topology, provider limits and design tokens.

- OpenRouter account probes no longer clear inference cooldowns. Real model
  response logs include a non-secret SHA-256 credential identifier, model,
  HTTP status and duration. HTTP 200 is not marked healthy until an image
  payload is returned. Ambiguous submissions are never repeated automatically.
- Unclassified 403 responses ask users to contact an administrator. Only
  explicitly classified content-policy failures recommend prompt refinement.
- Model responses expose best-effort historical wait estimates. At least five
  successful samples are required; at most 100 per model from the past 30 days
  are used. Queue pressure uses the configured provider concurrency, not a
  second hardcoded limit. Estimates are guidance, not SLA guarantees.
- Creation drafts, settings and local reference images are saved per account in
  IndexedDB. Typing writes metadata, not image bytes. Removing or uploading a
  reference releases its local file on the next draft save. Browser storage
  failures produce actionable notices. Drafts are local to the device.
- References show pending/uploading/validating/failed/ready states. Existing
  uploaded references are reused when retrying a partially failed submission.
- SSE remains the primary update path. Only active batches are reconciled every
  ten seconds. The list head is checked every minute, or every ten seconds when
  disconnected. Hidden pages stop polling; historical pages are not polled.
- The asset library virtualizes rows, including selection and folder controls.
  Source labels distinguish generated, uploaded and edited images.
- Failed wall thumbnails have a manual retry that does not resubmit generation.
- Editor saves use immutable document identity to detect concurrent edits;
  viewport-only reconciliation reuses compiled scene data. The cache is cleared
  when the renderer is destroyed.

## Verification

Regression coverage includes draft refresh, local-file cleanup, account
isolation, 2,000-asset virtualization, thumbnail retry, SSE fallback, editor
revision preservation, and provider cooldown preservation.

The estimate SQL was executed read-only against production data: 33 ms execution
time at the audit baseline. The existing 500-node renderer correctness/resource
gate passed locally. Software-renderer timings are not hardware 60 fps claims.

## Deployment and follow-up

No schema or capability migration is required. Build the API, Worker and Web
from one reviewed commit, pass CI/security scans, and deploy immutable release
digests after a database dump. Drain paid work before updating Worker. Update
API and Web, verify health, login, model estimates and the new interactions.
PostgreSQL and other Compose projects must not be restarted.

Monitor actual model response failures by credential and model, queue time,
execution time and manual duplicate submissions. Do not infer model access from
the account endpoint or increase BytePlus concurrency without account capacity
and observed throughput evidence.
