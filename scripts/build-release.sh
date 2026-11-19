#!/usr/bin/env bash
# Build a release tarball for the host platform:
#   rhymehub-<version>-<os>-<arch>.tar.gz containing bin/rhymehub, bin/rhymehub-supervisor and VERSION.
# Prints the sha256 of the tarball. Usage: scripts/build-release.sh <version> <outdir>
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <version> <outdir>" >&2
  exit 2
fi

version="$1"
outdir="$2"

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "error: version must look like 1.2.3 or 1.2.3-rc.1, got '$version'" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
name="rhymehub-${version}-${goos}-${goarch}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
stage="$work/$name"
mkdir -p "$stage/bin" "$outdir"

ldflags="-s -w -X main.version=${version}"
cd "$repo_root"
CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -ldflags "$ldflags" -o "$stage/bin/rhymehub" ./apps/server/cmd/rhymehub
CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -ldflags "$ldflags" -o "$stage/bin/rhymehub-supervisor" ./services/supervisor/cmd/rhymehub-supervisor

printf '%s\n' "$version" > "$stage/VERSION"

tarball="$outdir/$name.tar.gz"
# bin/ and VERSION sit at the archive root, which is the layout installVersion expects.
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$tarball" bin VERSION

if command -v shasum >/dev/null 2>&1; then
  digest="$(shasum -a 256 "$tarball" | awk '{print $1}')"
else
  digest="$(sha256sum "$tarball" | awk '{print $1}')"
fi

echo "$tarball"
echo "sha256: $digest"
