#!/usr/bin/env bash
set -euo pipefail

source_binary="${1:-./clipfit}"
install_dir="${CLIPFIT_INSTALL_DIR:-${GOBIN:-$HOME/.local/bin}}"
install_path="${install_dir}/clipfit"

if [[ ! -f "$source_binary" ]]; then
  printf 'clipfit: compiled binary not found: %s\n' "$source_binary" >&2
  printf 'Build it first with: go build -buildvcs=false -trimpath -o ./clipfit .\n' >&2
  exit 1
fi

if [[ ! -x "$source_binary" ]]; then
  printf 'clipfit: compiled binary is not executable: %s\n' "$source_binary" >&2
  exit 1
fi

mkdir -p "$install_dir"
install -m 0755 "$source_binary" "$install_path"

printf 'Installed clipfit to %s\n' "$install_path"

case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) printf 'Note: add %s to PATH to run clipfit directly.\n' "$install_dir" >&2 ;;
esac
