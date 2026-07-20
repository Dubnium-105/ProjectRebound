#!/usr/bin/env bash
set -euo pipefail

target=/etc/haproxy/cloudflare-ips.lst
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsS https://www.cloudflare.com/ips-v4 >>"$tmp"
printf '\n' >>"$tmp"
curl -fsS https://www.cloudflare.com/ips-v6 >>"$tmp"
printf '\n' >>"$tmp"
python3 - "$tmp" <<'PY'
import ipaddress
import pathlib
import sys

lines = [line.strip() for line in pathlib.Path(sys.argv[1]).read_text().splitlines() if line.strip()]
if len(lines) < 10:
    raise SystemExit("Cloudflare IP list is unexpectedly short")
for line in lines:
    ipaddress.ip_network(line)
PY
if [[ -f "$target" ]] && cmp -s "$tmp" "$target"; then
  exit 0
fi
install -o root -g haproxy -m 0640 "$tmp" "$target"
haproxy -c -f /etc/haproxy/haproxy.cfg
systemctl reload haproxy
