#!/usr/bin/env sh
set -eu

command -v openssl >/dev/null 2>&1 || {
  printf 'openssl is required\n' >&2
  exit 1
}
command -v xxd >/dev/null 2>&1 || {
  printf 'xxd is required\n' >&2
  exit 1
}

IFS= read -r private_seed_base64
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM
private_der="$temporary_dir/private.der"

# PKCS#8 Ed25519 prefix followed by the 32-byte RFC 8032 seed.
printf '%s' '302e020100300506032b657004220420' | xxd -r -p >"$private_der"
printf '%s' "$private_seed_base64" | openssl base64 -d -A >>"$private_der"
test "$(wc -c <"$private_der" | tr -d ' ')" = "48" || {
  printf 'input must be a base64-encoded 32-byte Ed25519 seed\n' >&2
  exit 1
}
openssl pkey -inform DER -in "$private_der" -pubout -outform DER 2>/dev/null |
  tail -c 32 |
  openssl base64 -A
printf '\n'
