#!/usr/bin/env bash
# Obsidian Task Runner — thin install wrapper
# Delegates to otg install. Keep this for backward compatibility.
set -euo pipefail

BINARY="otg"
if ! command -v "$BINARY" >/dev/null 2>&1; then
  if [ -f "./otg" ]; then
    BINARY="./otg"
  else
    echo "otg not found. Build with: make build" >&2
    echo "Then: make install  # copies otg to ~/.local/bin/" >&2
    exit 1
  fi
fi

exec "$BINARY" install "$@"
