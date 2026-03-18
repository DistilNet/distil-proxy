# scripts

Helper scripts for development, build, and release tasks.

- `coverage-gate.sh`: runs `go test -coverprofile` and enforces:
  - global line coverage `>= 85%`
  - critical package coverage `>= 95%` for `internal/ws`, `internal/daemon`, `internal/fetch`, `internal/config`
- `sync-public-release.sh`: fast-forwards the public `DistilNet/distil-proxy` repo to a tagged commit before you publish public assets and bump `distil-app`'s proxy release manifest.
