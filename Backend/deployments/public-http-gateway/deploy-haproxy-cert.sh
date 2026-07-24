#!/usr/bin/env bash
set -euo pipefail

gateway_defaults="/etc/default/projectrebound-http-gateway"
if [[ -r "$gateway_defaults" ]]; then
  # The file is root-owned and contains simple KEY=VALUE deployment settings.
  # shellcheck disable=SC1091
  source "$gateway_defaults"
fi
public_api_host="${PUBLIC_API_HOST:?set PUBLIC_API_HOST in $gateway_defaults}"
hosts=("$public_api_host")
if [[ -n "${ADMIN_WEB_HOST:-}" ]]; then
  hosts+=("$ADMIN_WEB_HOST")
fi
for host in "${hosts[@]}"; do
  live_dir="/etc/letsencrypt/live/$host"
  target="/etc/haproxy/certs/$host.pem"
  tmp="$(mktemp "/etc/haproxy/certs/$host.pem.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  test -s "$live_dir/fullchain.pem"
  test -s "$live_dir/privkey.pem"
  cat "$live_dir/fullchain.pem" "$live_dir/privkey.pem" >"$tmp"
  chown root:haproxy "$tmp"
  chmod 0640 "$tmp"
  mv -f "$tmp" "$target"
  trap - EXIT
done
haproxy -c -f /etc/haproxy/haproxy.cfg
systemctl reload haproxy
