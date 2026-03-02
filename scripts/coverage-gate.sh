#!/usr/bin/env bash
set -euo pipefail

GO_BIN="${GO_BIN:-go}"
COVERAGE_FILE="${COVERAGE_FILE:-coverage.out}"
GLOBAL_MIN="${GLOBAL_MIN:-85}"
CRITICAL_MIN="${CRITICAL_MIN:-95}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CRITICAL_PACKAGES=(
  "github.com/distilnet/distil-proxy/internal/ws"
  "github.com/distilnet/distil-proxy/internal/daemon"
  "github.com/distilnet/distil-proxy/internal/fetch"
  "github.com/distilnet/distil-proxy/internal/config"
)

# Some CI/sandbox environments do not allow writing to the default global Go cache.
if [[ -z "${GOCACHE:-}" ]]; then
  export GOCACHE="${REPO_ROOT}/.cache/go-build"
fi
if [[ -z "${GOMODCACHE:-}" ]]; then
  export GOMODCACHE="${REPO_ROOT}/.gomodcache"
fi
mkdir -p "${GOCACHE}"
mkdir -p "${GOMODCACHE}"

echo "running coverage profile..."
set +e
test_output="$("${GO_BIN}" test -coverprofile="${COVERAGE_FILE}" ./... 2>&1)"
test_exit=$?
set -e
echo "${test_output}"
if [[ "${test_exit}" -ne 0 ]]; then
  echo "coverage profile run failed"
  exit "${test_exit}"
fi

echo "evaluating global coverage gate..."
global_cov="$("${GO_BIN}" tool cover -func="${COVERAGE_FILE}" | awk '/^total:/ {gsub("%","",$3); print $3}')"
if [[ -z "${global_cov}" ]]; then
  echo "failed to compute global coverage"
  exit 1
fi

if ! awk -v v="${global_cov}" -v min="${GLOBAL_MIN}" 'BEGIN { exit ((v + 0) >= (min + 0) ? 0 : 1) }'; then
  echo "global coverage gate failed: ${global_cov}% < ${GLOBAL_MIN}%"
  exit 1
fi
echo "global coverage gate passed: ${global_cov}% >= ${GLOBAL_MIN}%"

echo "evaluating critical package coverage gates..."
for pkg in "${CRITICAL_PACKAGES[@]}"; do
  pkg_cov="$(echo "${test_output}" | awk -v pkg="${pkg}" '
    $1 == "ok" && $2 == pkg {
      for (i = 1; i <= NF; i++) {
        if ($i == "coverage:") {
          val = $(i + 1)
          gsub("%", "", val)
          print val
          exit
        }
      }
    }
  ')"

  if [[ -z "${pkg_cov}" ]]; then
    echo "failed to find coverage output for ${pkg}"
    exit 1
  fi

  if ! awk -v v="${pkg_cov}" -v min="${CRITICAL_MIN}" 'BEGIN { exit ((v + 0) >= (min + 0) ? 0 : 1) }'; then
    echo "critical coverage gate failed: ${pkg} ${pkg_cov}% < ${CRITICAL_MIN}%"
    exit 1
  else
    echo "critical coverage gate passed: ${pkg} ${pkg_cov}% >= ${CRITICAL_MIN}%"
  fi
done

echo "all coverage gates passed"
