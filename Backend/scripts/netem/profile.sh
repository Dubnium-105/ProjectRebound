#!/usr/bin/env sh
. "$(dirname "$0")/_common.sh"; require_test_interface
case "${1:-}" in
  mild) replace_netem delay 50ms 10ms loss 1% ;;
  moderate) replace_netem delay 120ms 30ms loss 5% rate 2mbit ;;
  severe) replace_netem delay 250ms 80ms loss 15% reorder 3% 50% rate 256kbit ;;
  *) echo "usage: $0 mild|moderate|severe" >&2; exit 2 ;;
esac
