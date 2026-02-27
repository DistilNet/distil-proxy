# scripts

Helper scripts for development, build, and release tasks.

- `coverage-gate.sh`: runs `go test -coverprofile` and enforces:
  - global line coverage `>= 85%`
  - critical package coverage `>= 95%` for `internal/ws`, `internal/daemon`, `internal/fetch`, `internal/config`
