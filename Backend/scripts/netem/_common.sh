#!/usr/bin/env sh
set -eu

require_test_interface() {
  if [ "${NETEM_I_UNDERSTAND:-}" != "isolated-test" ] || [ "$(id -u)" -ne 0 ]; then
    echo "requires root and NETEM_I_UNDERSTAND=isolated-test" >&2; exit 1
  fi
  interface="${NETEM_INTERFACE:-}"
  if ! printf '%s' "$interface" | grep -Eq '^(veth|dummy|br-test|lo)[A-Za-z0-9_.:-]*$'; then
    echo "NETEM_INTERFACE must be an explicit isolated veth/dummy/br-test/lo interface" >&2; exit 1
  fi
  if [ "${PROJECTREBOUND_ENVIRONMENT:-test}" = "production" ]; then
    echo "refusing to run netem in production" >&2; exit 1
  fi
}

replace_netem() { tc qdisc replace dev "$interface" root netem "$@"; }
