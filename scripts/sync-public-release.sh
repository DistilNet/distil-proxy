#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./scripts/sync-public-release.sh [--dry-run] [tag]

Sync a tagged distil-proxy commit to the public DistilNet repo before creating
the public GitHub release and bumping distil-app's proxy manifest.

Arguments:
  tag         Release tag to sync, for example v1.7.3. If omitted, the script
              uses RELEASE_TAG or the exact tag on HEAD.

Options:
  --dry-run   Validate the sync plan and print the push commands without
              mutating the public repo.
  -h, --help  Show this help text.
EOF
}

DRY_RUN=0
TAG="${RELEASE_TAG:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      if [[ -n "$TAG" ]]; then
        echo "only one release tag may be provided" >&2
        usage >&2
        exit 1
      fi
      TAG="$1"
      ;;
  esac
  shift
done

if [[ -z "$TAG" ]]; then
  TAG="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
fi

if [[ -z "$TAG" ]]; then
  echo "release tag required; pass one explicitly or run from an exact tagged commit" >&2
  exit 1
fi

PUBLIC_REMOTE_URL="${PUBLIC_REMOTE_URL:-https://github.com/DistilNet/distil-proxy.git}"
PUBLIC_RELEASE_REPO="${PUBLIC_RELEASE_REPO:-DistilNet/distil-proxy}"
PUBLIC_TARGET_BRANCH="${PUBLIC_TARGET_BRANCH:-master}"
COMMIT="$(git rev-list -n 1 "${TAG}^{commit}")"
TEMP_REF="refs/tmp/public-release-sync/${PUBLIC_TARGET_BRANCH}"

cleanup() {
  git update-ref -d "$TEMP_REF" >/dev/null 2>&1 || true
}

trap cleanup EXIT

run() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    printf '+'
    printf ' %q' "$@"
    printf '\n'
    return 0
  fi

  "$@"
}

git fetch --no-tags "$PUBLIC_REMOTE_URL" "refs/heads/${PUBLIC_TARGET_BRANCH}:${TEMP_REF}" >/dev/null
PUBLIC_BRANCH_COMMIT="$(git rev-parse "$TEMP_REF")"

if ! git merge-base --is-ancestor "$PUBLIC_BRANCH_COMMIT" "$COMMIT"; then
  echo "public ${PUBLIC_TARGET_BRANCH} is not an ancestor of ${TAG} (${COMMIT}); sync the public repo history manually first" >&2
  exit 1
fi

REMOTE_TAG_COMMIT="$(git ls-remote "$PUBLIC_REMOTE_URL" "refs/tags/${TAG}" | awk 'NR==1 { print $1 }')"
if [[ -n "$REMOTE_TAG_COMMIT" && "$REMOTE_TAG_COMMIT" != "$COMMIT" ]]; then
  echo "public tag ${TAG} already exists at ${REMOTE_TAG_COMMIT}, expected ${COMMIT}" >&2
  exit 1
fi

run git push "$PUBLIC_REMOTE_URL" "${COMMIT}:refs/heads/${PUBLIC_TARGET_BRANCH}"

if [[ -z "$REMOTE_TAG_COMMIT" ]]; then
  run git push "$PUBLIC_REMOTE_URL" "refs/tags/${TAG}:refs/tags/${TAG}"
fi

cat <<EOF
Public repo sync ready for ${TAG} (${COMMIT}).
Next:
  1. Build fresh artifacts from this commit: make build-artifacts checksums
  2. Create or update the public GitHub release in ${PUBLIC_RELEASE_REPO} with dist/* and dist/SHA256SUMS
  3. Bump distil-app/config/proxy_releases.json current.version and sha256 values after the public assets are live
EOF
