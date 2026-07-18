#!/usr/bin/env bash
set -euo pipefail

# Compatibility entry point. The old SQLite/systemd deployment no longer
# represents the PostgreSQL/Redis control plane and must not be used.
script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec "$script_dir/../scripts/deploy-control-plane.sh" "$@"
