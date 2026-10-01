#!/bin/sh
# Rebound one-command installer.
#   curl -fsSL https://raw.githubusercontent.com/vrlda/rebound/main/install.sh | sh -s -- mail.example.com
set -eu

DOMAIN="${1:-${DOMAIN:-}}"
DIR="${REBOUND_DIR:-$HOME/rebound}"
RAW="https://raw.githubusercontent.com/vrlda/rebound/main"

if [ -z "$DOMAIN" ]; then
  echo "Usage: sh install.sh <domain>   (e.g. mail.example.com, its DNS must point at this server)" >&2
  exit 1
fi
command -v docker >/dev/null 2>&1 || { echo "Docker is required: https://docs.docker.com/engine/install/" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required (docker compose ...)." >&2; exit 1; }

mkdir -p "$DIR" && cd "$DIR"
curl -fsSL "$RAW/docker-compose.yml" -o docker-compose.yml
curl -fsSL "$RAW/Caddyfile" -o Caddyfile
[ -f .env ] || printf 'DOMAIN=%s\nRESEND_API_KEY=\nACCOUNT_NAME=Default\n' "$DOMAIN" > .env

echo "Starting Rebound in $DIR ..."
docker compose pull --quiet
docker compose up -d

printf 'Waiting for the setup link'
i=0
while [ $i -lt 60 ]; do
  LINK=$(docker compose logs rebound 2>/dev/null | grep -o 'https://[^ ]*/setup#[A-Za-z0-9_-]*' | tail -1 || true)
  [ -n "$LINK" ] && break
  printf '.'; sleep 2; i=$((i + 1))
done
echo
if [ -n "${LINK:-}" ]; then
  echo "Rebound is running. Open this one-time link to set your password and 2FA:"
  echo "  $LINK"
else
  echo "Rebound started. Get your setup link with: cd $DIR && docker compose logs rebound | grep 'SETUP LINK'"
fi
