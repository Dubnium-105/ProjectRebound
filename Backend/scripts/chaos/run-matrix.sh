#!/usr/bin/env sh
set -eu

test_command="${CHAOS_TEST_COMMAND:-}"
if [ -z "$test_command" ]; then echo "CHAOS_TEST_COMMAND is required" >&2; exit 1; fi
script_dir="$(CDPATH= cd -- "$(dirname "$0")" && pwd)"
for scenario in "restart control-plane" "restart redis" "pause redis" "pause postgres" "sigkill control-plane"; do
  set -- $scenario
  "$script_dir/compose-fault.sh" "$1" "$2"
  sh -c "$test_command"
done
