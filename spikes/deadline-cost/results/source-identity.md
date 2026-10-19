# Reproduction source identity

The recorded measurements used the original revisions below. Later rebases
preserved API source but incorporated CommitWake in the common store. The probe
pins the historical store explicitly; these checks are not new measurements.

Original: `deff9a6827c0688d4acb9e5f6f98d49990b37058`
Reproduction API tree: `dbd18ef4b022648a45f7e82c7221e68640fd00a9`
Contained in ancestor `2a254336468974584663f66906d301aa5ba3e280`.

Source object comparisons exited 0. Checked objects:

- `internal/hub/api/session.go`: `349030b6e21641f8eeb99fbd6d3db8d5f85855f5`
- `internal/hub/api/session_create.go`: `fcbb0a4c9e2d13c284a612b6f14f3295fefa6f29`

Original: `4ffa40fa60dddbcb1c573d1df49e12e9862bd118`
Reproduction API tree: `515526b8925cab1dc1a647473ceee418b880d399`
Contained in ancestor `c43e552bc2ce39ffe61d56ce07e0ed30b825667b`.

Source object comparisons exited 0. Checked objects:

- `internal/hub/api`: `515526b8925cab1dc1a647473ceee418b880d399`
- `internal/hub/approvals`: `d9199e00f7f85d2511b2fd372d12c22d4a2d4993`
- `internal/hub/statedb`: `d3bcbfea1958ca114806008ecf510d53ee5ca88e`
- `internal/hub/outbox`: `d053226fd9e4f2070ae7790aaac58ac5ab2ec53a`
- `go.mod`: `17135987c8b544ef9dbcb415cf3dfe5697d953a6`
- `go.sum`: `bd820ad6699a61efac252ace3de234e6051d7198`

Original: `4ffa40fa60dddbcb1c573d1df49e12e9862bd118`
Reachable reproduction input: `6ac9e9b662aae4993831dbb9b2cbd966d8c6ceed`

Source object comparisons exited 0. Checked objects:

- `internal/store`: `1d424ac02ea7a18e829b9f36b4f2fd8086f75958`
- `go.mod`: `17135987c8b544ef9dbcb415cf3dfe5697d953a6`
- `go.sum`: `bd820ad6699a61efac252ace3de234e6051d7198`

`prepare.py` restores historical store.go, writer.go and doc.go, and supplies
an empty package file for the later commit.go through temporary Go overlays.
Both arms use that same historical store. The measured API replacements remain
byte-identical to their original measured files. CommitWake has its own evidence
in spikes/commitwake; combined current-stack latency is NOT_CHECKED.

The script selects API tree objects rather than pre-rebase commit IDs. Those
trees are in the branch history; a fresh single-branch clone can reproduce
the overlays without unmerged local refs. Original commit IDs above label the
measurements and are not inputs to the script. Fresh-clone RED/GREEN commands
and byte comparisons are recorded in reproduction-checks.txt.
