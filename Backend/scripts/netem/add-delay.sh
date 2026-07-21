#!/usr/bin/env sh
. "$(dirname "$0")/_common.sh"; require_test_interface
replace_netem delay "${NETEM_DELAY:-50ms}"
