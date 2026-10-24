# web

Read the root `AGENTS.md` first; it applies here. This directory is two
things: the React dashboard (`src/`, Node only) and the Go package that
embeds its build output for `fleetd`. Load `fleet-frontend` for `src/`,
`fleet-go` for the `.go` files.

- `web/dist` is build output. Only the placeholder `web/dist/index.html` is
  committed; it keeps `go build` working without Node. Never commit a built
  page or its `assets/`: `npm run e2e` builds, then restores the placeholder.
- The embed pattern is `//go:embed all:dist` in `dashboard.go`. A missing
  `web/dist` fails the build; the placeholder keeps it present.
- `Handler` serves exact files, falls back to `index.html` for client
  routes, and refuses non-GET/HEAD. It sits behind the Host/Origin guard in
  `internal/hub/api` and reads no credentials; a change here updates this
  README and `doc.go` in the same commit.
