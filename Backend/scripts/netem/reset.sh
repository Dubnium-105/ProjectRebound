#!/usr/bin/env sh
. "$(dirname "$0")/_common.sh"; require_test_interface
tc qdisc del dev "$interface" root 2>/dev/null || true
