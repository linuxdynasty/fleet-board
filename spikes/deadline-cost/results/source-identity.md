# Reproduction source identity

The recorded measurements used the original revisions below. Later rebases
preserved API source but incorporated CommitWake in the common store. The probe
pins the historical store explicitly; these checks are not new measurements.

Original: `deff9a6827c0688d4acb9e5f6f98d49990b37058`
Reachable reproduction input: `7502ecd932ed1be243ba2d2be0922e5702a3451b`

Source diff and ancestry commands exited 0. Checked objects:

- `internal/hub/api/session.go`: `349030b6e21641f8eeb99fbd6d3db8d5f85855f5`
- `internal/hub/api/session_create.go`: `fcbb0a4c9e2d13c284a612b6f14f3295fefa6f29`

Original: `4ffa40fa60dddbcb1c573d1df49e12e9862bd118`
Reachable reproduction input: `a1ecb102dcbfccc2b5ad569fcfe3d71c9464316a`

Source diff and ancestry commands exited 0. Checked objects:

- `internal/hub/api`: `515526b8925cab1dc1a647473ceee418b880d399`
- `internal/hub/approvals`: `d9199e00f7f85d2511b2fd372d12c22d4a2d4993`
- `internal/hub/statedb`: `d3bcbfea1958ca114806008ecf510d53ee5ca88e`
- `internal/hub/outbox`: `d053226fd9e4f2070ae7790aaac58ac5ab2ec53a`
- `go.mod`: `17135987c8b544ef9dbcb415cf3dfe5697d953a6`
- `go.sum`: `bd820ad6699a61efac252ace3de234e6051d7198`

Original: `4ffa40fa60dddbcb1c573d1df49e12e9862bd118`
Reachable reproduction input: `6ac9e9b662aae4993831dbb9b2cbd966d8c6ceed`

Source diff and ancestry commands exited 0. Checked objects:

- `internal/store`: `1d424ac02ea7a18e829b9f36b4f2fd8086f75958`
- `go.mod`: `17135987c8b544ef9dbcb415cf3dfe5697d953a6`
- `go.sum`: `bd820ad6699a61efac252ace3de234e6051d7198`

`prepare.py` restores historical store.go, writer.go and doc.go, and supplies
an empty package file for the later commit.go through temporary Go overlays.
Both arms use that same historical store. The measured API replacements remain
byte-identical to their original measured files. CommitWake has its own evidence
in spikes/commitwake; combined current-stack latency is NOT_CHECKED.
