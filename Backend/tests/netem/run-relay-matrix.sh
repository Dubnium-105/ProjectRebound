#!/usr/bin/env sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root inside an isolated Linux relay test host" >&2
  exit 1
fi

interface="${NETEM_INTERFACE:-}"
test_command="${NETEM_TEST_COMMAND:-}"
if ! printf '%s' "$interface" | grep -Eq '^[A-Za-z0-9_.:-]+$'; then
  echo "NETEM_INTERFACE must name one explicit test interface" >&2
  exit 1
fi
if [ -z "$test_command" ]; then
  echo "NETEM_TEST_COMMAND must invoke the relay integration test" >&2
  exit 1
fi

cleanup() {
  tc qdisc del dev "$interface" root 2>/dev/null || true
}
trap cleanup EXIT INT TERM

run_profile() {
  name="$1"
  shift
  cleanup
  tc qdisc add dev "$interface" root netem "$@"
  echo "running netem profile: $name"
  sh -c "$test_command"
}

run_profile delay-low delay 50ms 20ms distribution normal
run_profile delay-high delay 300ms 100ms distribution normal
run_profile loss-low loss 1%
run_profile loss-high loss 5%
run_profile reorder delay 100ms 30ms reorder 10% 50%
run_profile duplicate delay 80ms 20ms duplicate 2%
run_profile constrained rate 2mbit delay 150ms 50ms loss 2%

cleanup
echo "running short disconnect profile"
tc qdisc add dev "$interface" root netem loss 100%
sleep 5
cleanup
sh -c "$test_command"
