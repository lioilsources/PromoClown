#!/usr/bin/env bash
# Wire PromoClown into an installed OpenClaw on the Spark. Safe to re-run.
#
# Run on the Spark (deploy/spark/deploy.sh copies it to ~/.config/promoclown/)
# once these are done:
#   - OpenClaw installed, ~/.openclaw/openclaw.json written from openclaw.json5.example
#   - ~/.config/promoclown/openclaw.env and promo.env filled in
#   - the gateway service installed (openclaw gateway install)
#
# OpenClaw CLI flags move between releases; if a step fails, compare with
# `openclaw <command> --help` and docs/RUNBOOK.md.
set -euo pipefail

cfg="$HOME/.config/promoclown"
set -a
# shellcheck disable=SC1091
. "$cfg/openclaw.env"
set +a
: "${TELEGRAM_OWNER_ID:?set TELEGRAM_OWNER_ID in $cfg/openclaw.env}"
export PATH="$HOME/.local/bin:$HOME/.openclaw/bin:$PATH"

echo "==> exec allowlist: only the PromoClown CLIs"
for bin in promo promo-ingest store-reviews youtube-comments reddit-monitor bluesky-mentions; do
    openclaw approvals allowlist add --agent main "$HOME/.local/bin/$bin"
done

echo "==> heartbeat checklist"
# HEARTBEAT.md is no longer read at runtime; doctor --fix moves it into the
# heartbeat job's scratch.
openclaw doctor --fix

echo "==> cron jobs"
existing=$(openclaw cron list --all 2>/dev/null || true)
add_job() { # add_job <name> <openclaw cron add flags...>
    local name=$1
    shift
    if grep -qF "$name" <<<"$existing"; then
        echo "    '$name' already exists; to change it: openclaw cron list --all, then openclaw cron edit <id>"
        return
    fi
    openclaw cron add --name "$name" "$@"
    echo "    added '$name'"
}

add_job "promo weekly-plan" \
    --cron "0 8 * * 1" --tz Europe/Prague --exact \
    --session isolated --agent main \
    --message "$(cat "$cfg/cron/weekly-plan.md")" \
    --announce --channel telegram --to "$TELEGRAM_OWNER_ID"

add_job "promo daily-digest" \
    --cron "0 19 * * *" --tz Europe/Prague --exact \
    --session isolated --light-context --agent main \
    --message "$(cat "$cfg/cron/daily-digest.md")" \
    --announce --channel telegram --to "$TELEGRAM_OWNER_ID"

echo "==> restart gateway"
systemctl --user daemon-reload
systemctl --user restart openclaw-gateway.service

cat <<EOF

Done. Check:
  openclaw cron list --all            # weekly-plan, daily-digest, Heartbeat (main)
  openclaw approvals get              # the six PromoClown binaries
  openclaw skills list                # promo, store-reviews, youtube-comments, reddit-monitor, bluesky-mentions
  openclaw cron run <weekly-plan id> --wait
EOF
