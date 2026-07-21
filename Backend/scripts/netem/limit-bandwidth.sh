#!/usr/bin/env sh
. "$(dirname "$0")/_common.sh"; require_test_interface
replace_netem rate "${NETEM_RATE:-2mbit}"
