#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
unit="$repo/examples/systemd/goosecab-gateway.service"
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

for setting in \
  'User=goosecab' \
  'StateDirectory=goosecab' \
  'Environment=GOOSECAB_DB=/var/lib/goosecab/goosecab.db' \
  'Environment=GOOSECAB_ADDR=127.0.0.1:8312' \
  'EnvironmentFile=/etc/goosecab/gateway.env' \
  'ExecStart=/usr/local/bin/goosecab-server' \
  'StartLimitIntervalSec=0' \
  'Restart=on-failure' \
  'RestartSec=5s' \
  'WantedBy=multi-user.target'
do
  if ! grep -Fxq -- "$setting" "$unit"; then
    printf 'Missing service setting %s\n' "$setting" >&2
    exit 1
  fi
done

# Validate a temporary copy with an existing executable. No unit is installed or started.
sed 's|^ExecStart=/usr/local/bin/goosecab-server$|ExecStart=/bin/true|' \
  "$unit" > "$scratch/goosecab-gateway.service"
mkdir "$scratch/runtime"
XDG_RUNTIME_DIR="$scratch/runtime" systemd-analyze --user verify --man=no --generators=no \
  "$scratch/goosecab-gateway.service"
printf 'PASS: systemd Gateway unit, boot target, restart policy and state paths\n'
