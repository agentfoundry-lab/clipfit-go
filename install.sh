#!/usr/bin/env bash
set -euo pipefail

if (($# > 1)); then
  printf 'Usage: %s [compiled-linux-binary]\n' "$0" >&2
  exit 2
fi

repo_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if (($# == 1)); then
  source_binary="$1"
else
  goarch="${CLIPFIT_GOARCH:-$(go env GOARCH)}"
  source_binary="$repo_dir/dist/linux-${goarch}/clipfit"
fi

install_dir="${CLIPFIT_INSTALL_DIR:-${GOBIN:-$HOME/.local/bin}}"
install_path="${install_dir}/clipfit"

if [[ ! -f "$source_binary" ]]; then
  printf 'clipfit: compiled Linux binary not found: %s\n' "$source_binary" >&2
  printf 'Build both platform binaries first with: ./build.sh\n' >&2
  exit 1
fi

if [[ ! -x "$source_binary" ]]; then
  printf 'clipfit: compiled Linux binary is not executable: %s\n' "$source_binary" >&2
  exit 1
fi

mkdir -p "$install_dir"
install -m 0755 "$source_binary" "$install_path"

printf 'Installed clipfit to %s\n' "$install_path"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) printf 'Note: add %s to PATH to run clipfit directly.\n' "$install_dir" >&2 ;;
esac
