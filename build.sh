#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
goarch="${CLIPFIT_GOARCH:-$(go env GOARCH)}"
dist_dir="${CLIPFIT_DIST_DIR:-$repo_dir/dist}"

build_target() {
  local goos="$1"
  local binary_name="$2"
  local target_dir="$dist_dir/${goos}-${goarch}"
  local target_path="$target_dir/$binary_name"

  mkdir -p "$target_dir"
  printf 'Building %s/%s -> %s\n' "$goos" "$goarch" "$target_path"
  (
    cd "$repo_dir"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -buildvcs=false -trimpath -o "$target_path" .
  )
}

build_target linux clipfit
build_target windows clipfit.exe

printf 'Build complete: %s\n' "$dist_dir"
