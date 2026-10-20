#!/usr/bin/env bash
set -euo pipefail
: "${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY}"
: "${RELEASE_TAG:?Set RELEASE_TAG}"
[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9.-]+)?$ ]] || { echo 'Invalid release tag' >&2; exit 1; }
(cd dist && sha256sum --check checksums.txt)
existing="$(gh api "repos/$GITHUB_REPOSITORY/releases/tags/$RELEASE_TAG" --jq '.assets[].name')"
for file in dist/*; do
  [ -f "$file" ] || continue
  name="$(basename "$file")"
  if printf '%s\n' "$existing" | grep -Fxq "$name"; then
    echo "Preserving existing release asset: $name"
    continue
  fi
  gh release upload "$RELEASE_TAG" "$file" --repo "$GITHUB_REPOSITORY"
done
