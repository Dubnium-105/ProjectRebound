#!/usr/bin/env sh
. "$(dirname "$0")/_common.sh"; require_test_interface
replace_netem duplicate "${NETEM_DUPLICATE:-2%}"
