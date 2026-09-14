#!/usr/bin/env bash
# Install PromoClown's Spark side: skill CLIs, SKILL.md files, workspace files,
# cron prompts and user units. Secrets are never copied from here.
#
#   make deploy-spark        # builds the linux/arm64 CLIs first
set -euo pipefail

host=${1:-spark}
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
bins="$repo/bin/linux-arm64"
spark="$repo/deploy/spark"

if [ ! -x "$bins/promo" ]; then
    echo "no linux/arm64 binaries in $bins; run make spark-bins" >&2
    exit 1
fi

ssh "$host" 'mkdir -p ~/.local/bin ~/.openclaw/skills/promoclown ~/.openclaw/workspace \
    ~/.config/promoclown/cron ~/.config/systemd/user/openclaw-gateway.service.d \
    && chmod 700 ~/.config/promoclown'

echo "==> CLIs → ~/.local/bin"
rsync -a "$bins/" "$spark/promo-ingest" "$host:.local/bin/"

echo "==> skills → ~/.openclaw/skills/promoclown"
rsync -a --delete "$repo/skills/" "$host:.openclaw/skills/promoclown/"

# No --delete: PROJECTS.md, MEMORY.md and memory/ belong to the Spark.
echo "==> workspace files → ~/.openclaw/workspace"
rsync -a "$repo/workspace/" "$host:.openclaw/workspace/"

echo "==> cron prompts, config patches, setup script, env templates → ~/.config/promoclown"
rsync -a --delete "$spark/cron/" "$host:.config/promoclown/cron/"
rsync -a --delete "$spark/openclaw-patches/" "$host:.config/promoclown/openclaw-patches/"
rsync -a "$spark/setup-openclaw.sh" "$spark/promo.env.example" "$spark/openclaw.env.example" \
    "$spark/openclaw.json5.example" "$host:.config/promoclown/"

echo "==> systemd user units"
rsync -a "$spark/systemd/promo-refresh-projects.service" "$spark/systemd/promo-refresh-projects.timer" \
    "$host:.config/systemd/user/"
rsync -a "$spark/systemd/openclaw-gateway.service.d/" "$host:.config/systemd/user/openclaw-gateway.service.d/"

ssh "$host" bash -s <<'REMOTE'
set -euo pipefail
cfg=~/.config/promoclown
for f in promo.env openclaw.env; do
    if [ ! -f "$cfg/$f" ]; then
        cp "$cfg/$f.example" "$cfg/$f"
        echo "created $cfg/$f from the example: fill it in"
    fi
    chmod 600 "$cfg/$f"
done
# The gateway token never needs to be typed by anyone; generate it on first deploy.
if grep -q '^OPENCLAW_GATEWAY_TOKEN=$' "$cfg/openclaw.env"; then
    sed -i "s/^OPENCLAW_GATEWAY_TOKEN=$/OPENCLAW_GATEWAY_TOKEN=$(openssl rand -hex 32)/" "$cfg/openclaw.env"
    echo "generated OPENCLAW_GATEWAY_TOKEN in $cfg/openclaw.env"
fi
chmod +x "$cfg/setup-openclaw.sh"

# First-run ritual of a fresh workspace; ours arrives configured.
rm -f ~/.openclaw/workspace/BOOTSTRAP.md

systemctl --user daemon-reload
if grep -q '^PROMO_TOKEN=..*' "$cfg/promo.env"; then
    systemctl --user enable --now promo-refresh-projects.timer >/dev/null
    if systemctl --user start promo-refresh-projects.service; then
        echo "PROJECTS.md refreshed"
    else
        echo "PROJECTS.md refresh failed: journalctl --user -u promo-refresh-projects" >&2
    fi
else
    echo "PROMO_TOKEN is empty: PROJECTS.md timer not enabled yet"
fi

if systemctl --user cat openclaw-gateway.service >/dev/null 2>&1; then
    systemctl --user restart openclaw-gateway.service
    echo "openclaw-gateway restarted"
else
    echo "openclaw-gateway.service not installed yet (docs/RUNBOOK.md, phase A2)"
fi
REMOTE
