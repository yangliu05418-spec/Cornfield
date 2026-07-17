# Web

TanStack Start is used only as a statically built SPA with a prerendered public landing page.

- `/` → `dist/client/index.html`
- `/app/*` → `dist/client/_shell.html`
- All business data comes from the same-origin Go API under `/api/v1`.
- No server functions, server routes, database clients, or Node production process are allowed.

```bash
pnpm install
pnpm typecheck
pnpm test
pnpm build
```

The final Nginx image copies only `dist/client`; `dist/server` remains a build-time prerender implementation detail and is not copied into production.
