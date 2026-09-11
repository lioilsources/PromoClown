#!/usr/bin/env bash
# Deploy promo-api to JODA.
#
#   deploy/promo-api/deploy.sh [joda]
#
# Cross-compiles the linux/amd64 binary here and ships only this directory:
# JODA (3.8 GB RAM, swap full) should not run Go builds, so its image is just
# distroless plus that binary. deploy/promo-api/.env must already exist on
# JODA; it is never copied from here.
set -euo pipefail

host=${1:-joda}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
dest=deploy/PromoClown/deploy/promo-api
version=$(git -C "$repo" describe --tags --always --dirty 2>/dev/null || echo dev)

echo "==> build promo-api $version (linux/amd64)"
mkdir -p "$here/bin"
(cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$here/bin/promo-api" ./cmd/promo-api)

echo "==> sync to $host:$dest"
ssh "$host" "mkdir -p $dest"
rsync -a --exclude .env --exclude data "$here/" "$host:$dest/"

ssh "$host" bash -s -- "$dest" <<'REMOTE'
set -euo pipefail
cd "$1"
if [ ! -f .env ]; then
    echo "missing $1/.env: cp .env.example .env, fill it in, chmod 600" >&2
    exit 1
fi
chmod 600 .env
mkdir -p data
docker compose up -d --build
for _ in $(seq 1 20); do
    if docker compose exec -T promo-api promo-api healthcheck 2>/dev/null; then
        break
    fi
    sleep 1
done
docker compose exec -T promo-api promo-api check
REMOTE
