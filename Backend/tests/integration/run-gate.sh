#!/usr/bin/env bash
set -euo pipefail

if [[ "${V11_INTEGRATION_I_UNDERSTAND:-}" != "disposable-docker-stack" ]]; then
  echo "Refusing to run: set V11_INTEGRATION_I_UNDERSTAND=disposable-docker-stack." >&2
  exit 2
fi

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "The V1.1 container gate requires a Linux Docker host." >&2
  exit 2
fi

export V11_INTEGRATION=1
export TESTCONTAINERS_RYUK_DISABLED="${TESTCONTAINERS_RYUK_DISABLED:-true}"
status=0
go test -tags=integration -v -count=1 -timeout="${V11_INTEGRATION_TIMEOUT:-15m}" ./... || status=$?
if [[ -n "${V11_GATE_RESULT_FILE:-}" ]]; then
  printf '%s\n' "$status" > "$V11_GATE_RESULT_FILE"
fi
exit "$status"
