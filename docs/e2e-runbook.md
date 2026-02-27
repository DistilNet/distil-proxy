# Local E2E Runbook

This runbook validates daemon end-to-end behavior against a local websocket endpoint.

## Preconditions

- Go toolchain installed
- Repository dependencies downloaded (`go mod download`)

## Steps

1. From the `distil-proxy` repo root, run:

```bash
go test ./test/e2e -run TestDaemonRoundtripWithLocalWebsocketEndpoint -v
```

2. Confirm test output includes:
   - daemon startup in foreground mode
   - websocket connection to local endpoint
   - successful fetch `result` message with body `e2e-ok`

## What this verifies

- local daemon process can start (`start --foreground` runtime path)
- daemon connects to a local websocket endpoint with `X-Distil-Key` auth header
- daemon receives a `fetch` job and returns a successful `result` payload
