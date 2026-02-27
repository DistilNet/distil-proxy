# Contributing

## Requirements

- Go 1.25+
- `golangci-lint`
- `govulncheck`

## Setup

```bash
make build
```

## Development Loop

```bash
make test
make test-race
make lint
make vuln
```

## Pull Requests

- Keep changes small and reviewable.
- Add or update tests for behavior changes.
- Keep docs in sync with code.
- Ensure CI passes before requesting review.
