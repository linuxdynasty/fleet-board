# Fleet dashboard (`web/`)

React 19 with strict TypeScript and Vite. Built to `web/dist`, embedded in
`fleetd`; Node is a build-time dependency only. The API client is generated
from `fleet.hub.v1` (Connect): `make gen` at the repo root regenerates
`gen/ts`, and this app imports it relatively (`../../gen/ts`).

## Commands (in `web/`)

```bash
make gen               # at the repo root first: regenerates gen/ts
npm install            # once: exact pins in package.json; postinstall links
                       # node_modules into gen/ts for the generated imports
npm run typecheck      # tsc --noEmit
npm run lint           # eslint, no warnings
npm test               # vitest
npm run build          # vite build to web/dist
```

TypeScript is 6.0.x, the ADR 0020 fallback: typescript-eslint does not
parse TS 7 yet. Type-aware lint rules wait on that support; tsc is the
type gate until then.

## Conventions

- No router package: `src/router.ts` reads the `#/name` part of the
  address (ADR 0020). Plain CSS with CSS Modules, no framework.
- One `FleetService.Watch` stream per tab (`src/watch.ts`): the resume
  token continues the stream, `RESYNC_REQUIRED` drops local state and
  reopens with `send_initial`, reconnects back off with jitter and show a
  "reconnecting" state instead of stale data.
- Server state goes through `@tanstack/react-query`; views dedupe Watch
  changes by `(id, version)`.
- Plain words in the UI; raw contract names behind a details toggle.
  Untrusted text renders as text, never as HTML.
