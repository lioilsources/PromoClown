#!/usr/bin/env bash
# Copy the Postiz stack to a host and start it.
#
#   deploy/postiz/deploy.sh spark     # recommended: JODA lacks the RAM
#
# Needs deploy/postiz/.env and cloudflared/{config.yml,credentials.json} on the
# target already (they are never copied from here).
set -euo pipefail

host=${1:?usage: $0 <ssh host>}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
dest=deploy/PromoClown/deploy/postiz

ssh "$host" "mkdir -p $dest/cloudflared $dest/temporal"
rsync -a --exclude .env --exclude 'cloudflared/config.yml' --exclude 'cloudflared/*.json' \
    "$here/" "$host:$dest/"

ssh "$host" bash -s -- "$dest" <<'REMOTE'
set -euo pipefail
cd "$1"
missing=0
for f in .env cloudflared/config.yml cloudflared/credentials.json; do
    if [ ! -f "$f" ]; then
        echo "missing $1/$f (see the .example files and docs/RUNBOOK.md)" >&2
        missing=1
    fi
done
[ "$missing" = 0 ] || exit 1
chmod 600 .env
# The cloudflared image runs as uid 65532 (nonroot); a 0600 file owned by the
# deploying user is unreadable inside the container and the connector
# crash-loops with "permission denied". The directory is user-private anyway.
chmod 644 cloudflared/credentials.json cloudflared/config.yml
docker compose pull --quiet
docker compose up -d
docker compose ps
REMOTE
