#!/usr/bin/env bash
set -euo pipefail
build_root=/tmp/rebound-e2e21-redis7411-20260907
mkdir -p "$build_root"
cd "$build_root"
curl --fail --location --silent --show-error --output redis-7.4.11.tar.gz https://download.redis.io/releases/redis-7.4.11.tar.gz
curl --fail --location --silent --show-error --output upstream-hashes.txt https://raw.githubusercontent.com/redis/redis-hashes/master/README
expected=$(awk '$1 == "hash" && $2 == "redis-7.4.11.tar.gz" && $3 == "sha256" {print $4}' upstream-hashes.txt)
test "${#expected}" -eq 64
printf '%s  %s\n' "$expected" redis-7.4.11.tar.gz | sha256sum --check
tar -xzf redis-7.4.11.tar.gz
make -C redis-7.4.11 -j4 MALLOC=libc BUILD_TLS=no REDIS_CFLAGS= REDIS_LDFLAGS= redis-server redis-cli
redis-7.4.11/src/redis-server --version
sha256sum redis-7.4.11/src/redis-server redis-7.4.11/src/redis-cli
