# Reproduction source identity

The recorded measurements used the original revisions below. The API branch
was later rebased. These reachable revisions contain identical measured source;
this is an object comparison, not another measurement.

Original: `deff9a6827c0688d4acb9e5f6f98d49990b37058`
Reachable reproduction: `40b8527405774aa189bef1c902c0bf2efa6d57a9`

`git diff --exit-code OLD NEW -- PATHS` and ancestry checks both exited 0.

- `internal/hub/api/session.go`: `349030b6e21641f8eeb99fbd6d3db8d5f85855f5`
- `internal/hub/api/session_create.go`: `fcbb0a4c9e2d13c284a612b6f14f3295fefa6f29`

Original: `4ffa40fa60dddbcb1c573d1df49e12e9862bd118`
Reachable reproduction: `7d8b2890a89f9ac35e0b2a7af7586a1eecc5a509`

`git diff --exit-code OLD NEW -- PATHS` and ancestry checks both exited 0.

- `internal/hub/api`: `515526b8925cab1dc1a647473ceee418b880d399`
- `internal/hub/approvals`: `d9199e00f7f85d2511b2fd372d12c22d4a2d4993`
- `internal/hub/statedb`: `d3bcbfea1958ca114806008ecf510d53ee5ca88e`
- `internal/hub/outbox`: `d053226fd9e4f2070ae7790aaac58ac5ab2ec53a`
- `internal/store`: `1d424ac02ea7a18e829b9f36b4f2fd8086f75958`
- `go.mod`: `17135987c8b544ef9dbcb415cf3dfe5697d953a6`
- `go.sum`: `bd820ad6699a61efac252ace3de234e6051d7198`
