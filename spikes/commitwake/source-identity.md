# Measured source after rebase

The original candidate measured on 2026-10-18 was local commit
`ebc58249174acf310572eaabd00294743b259a6f`. Rebasing removed it from the
published branch history. Commit `966a5b4cce07aaf6b2fc50015afc649ec5dbabdb`
is an ancestor of the rebased branch and contains identical benchmark packages
and module inputs. It can be checked out from the published PR branch.
The original raw samples remain measurements of the original revision.

The coordinator checked these commands after rebase; both exited 0:

```sh
git diff --quiet ebc58249174acf310572eaabd00294743b259a6f 966a5b4cce07aaf6b2fc50015afc649ec5dbabdb -- go.mod go.sum internal/store internal/hub/ingest
git merge-base --is-ancestor 966a5b4cce07aaf6b2fc50015afc649ec5dbabdb HEAD
```

Both revisions have these exact Git object IDs:

| Input | Object ID |
|---|---|
| internal/store tree | `1bcfde3fd72610a9a25c8e246449775745d6953a` |
| internal/hub/ingest tree | `1494a98534f3741570bfb826c215a4ea6a1b7049` |
| go.mod | `17135987c8b544ef9dbcb415cf3dfe5697d953a6` |
| go.sum | `bd820ad6699a61efac252ace3de234e6051d7198` |

A fresh clone needs only the reachable revision and the reproduction commands
in the report. The old local revision is named for honest sample provenance;
it is not needed to build the reproduction candidate. Later cancellation-test
and documentation follow-ups do not change the measured runtime code.
These checks do not qualify active Watch, fan-out, Linux, or a cold OS cache.
