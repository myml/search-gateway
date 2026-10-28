#!/usr/bin/env bash
set -euo pipefail

IMAGE="${1:-ghcr.io/myml/search-gateway:latest}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Optional server-local settings; see deploy.env.example. Keeping these values
# outside the repository avoids publishing a real domain or host layout.
ENV_FILE="${SEARCH_GATEWAY_DEPLOY_ENV:-/etc/search-gateway/deploy.env}"
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
fi

ROOT="${SEARCH_GATEWAY_ROOT:-/srv/search-gateway}"
DATA="${SEARCH_GATEWAY_DATA:-$ROOT/data}"
CONFIG="${SEARCH_GATEWAY_CONFIG:-$ROOT/config.json}"
TRAEFIK_DIR="${SEARCH_GATEWAY_TRAEFIK_DIR:-/etc/traefik/providers}"
DOMAIN="${SEARCH_GATEWAY_DOMAIN:-search.example.com}"

if [ "$(id -u)" -ne 0 ]; then
  echo "deploy.sh must run as root (podman/systemd)" >&2
  exit 1
fi
if [ ! -f "$CONFIG" ]; then
  echo "missing $CONFIG; provision config.json and data once before the first deploy" >&2
  exit 1
fi
install -d -m 0755 "$DATA"
install -d -m 0755 "$TRAEFIK_DIR"

install -m 0644 "$DIR/container-search-gateway.service" /etc/systemd/system/container-search-gateway.service
sed -i \
  -e "s|@IMAGE@|$IMAGE|g" \
  -e "s|@ROOT@|$ROOT|g" \
  -e "s|@DATA_DIR@|$DATA|g" \
  -e "s|@CONFIG_FILE@|$CONFIG|g" \
  /etc/systemd/system/container-search-gateway.service

sed "s|@DOMAIN@|$DOMAIN|g" "$DIR/traefik.yaml" > "$TRAEFIK_DIR/search-gateway.yaml"
chmod 0644 "$TRAEFIK_DIR/search-gateway.yaml"

systemctl daemon-reload
systemctl enable container-search-gateway
systemctl restart container-search-gateway

for _ in $(seq 1 30); do
  if curl -fsS --max-time 2 http://127.0.0.1:18787/healthz >/dev/null; then
    echo "search-gateway is healthy on 127.0.0.1:18787"
    exit 0
  fi
  sleep 1
done

echo "search-gateway failed health check" >&2
systemctl status container-search-gateway --no-pager >&2 || true
podman logs --tail 100 search-gateway >&2 || true
exit 1
