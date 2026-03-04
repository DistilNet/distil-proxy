# distil-proxy

Public Go daemon for Distil proxy routing.

`distil-proxy` runs on a user's machine, keeps a websocket connection to `wss://proxy.distil.net/ws`, receives fetch jobs, executes requests from the user's network, and returns results.

## Features

- `dk_` API key based auth (single credential model)
- daemon lifecycle commands: `start`, `stop`, `restart`, `status`, `uninstall`
- manual binary upgrade command: `upgrade` (with daemon post-upgrade hook)
- service manager helpers: `service install`, `service uninstall`
- foreground mode for service managers: `start --foreground`
- bounded fetch execution with timeout + max response size guardrails
- websocket heartbeat and reconnect backoff
- structured JSON logs (`~/.distil-proxy/logs/daemon.log`)

## Install (local development)

```bash
make build
make install-local
```

This installs the binary to `~/.distil-proxy/bin/distil-proxy`.

## Quick Start

```bash
distil-proxy auth dk_your_api_key
distil-proxy start
distil-proxy upgrade
distil-proxy status
distil-proxy logs -n 50
distil-proxy service install
distil-proxy stop
```

## Runtime Files

```text
~/.distil-proxy/
  bin/distil-proxy
  config.json
  logs/daemon.log
  distil-proxy.pid
  status.json
  upgrade.json
```

`config.json` keys:
- `api_key` (required, `dk_` prefixed)
- `server` (default `wss://proxy.distil.net/ws`)
- `auto_upgrade` (default `true`)
- `upgrade_check_hours` (default `6`)

## Build and Test

```bash
make test
make test-race
make build
make build-artifacts
make checksums
```

## Website Installer Releases

`distil-app` `/install` uses these artifacts when running in development:

```bash
dist/distil-proxy-darwin-amd64
dist/distil-proxy-darwin-arm64
dist/distil-proxy-linux-amd64
dist/distil-proxy-linux-arm64
dist/distil-proxy-windows-amd64.exe
dist/distil-proxy-windows-arm64.exe
```

After changing CLI output or runtime behavior, rebuild before testing installer flows:

```bash
make build-artifacts
```

For non-development environments, `distil-app/config/proxy_releases.json` must be updated to the new GitHub release URLs and checksums, or installs will keep pulling older binaries.

## Project Layout

```text
cmd/distil-proxy        # entrypoint
internal/cli            # cobra command wiring
internal/config         # config schema/load/save/validation
internal/daemon         # runtime lifecycle and process state
internal/ws             # websocket protocol client
internal/fetch          # HTTP fetch executor
internal/jobs           # in-flight job registry
internal/observability  # logging and metrics
internal/platform       # launchd/systemd helpers
```

## Status

This repository is under active development. Interfaces may change before first stable release.
