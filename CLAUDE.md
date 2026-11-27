# PromoClown — CLAUDE.md

## Overview

Promo assistant for Ol1n's apps. An OpenClaw agent on the DGX Spark proposes
posts and reports reviews and mentions over Telegram; nothing is published
until a human approves the exact text in a separate Telegram approval bot.
Implements `Prompts/00-OPENCLAW_PROMO_PLAN.md`; deviations and the reasons for
them are listed in README.md.

```
Spark (arm64)                          JODA (amd64, 3.8 GB RAM)
  vLLM qwen36-agent ← LiteLLM ← gateway     promo-api container
  OpenClaw gateway (loopback)                 ├─ SQLite + HTTP API :8094 (LAN)
    skills: promo, store-reviews,             ├─ Telegram approval bot
    youtube-comments, reddit-monitor,         └─ publisher → Postiz Public API
    bluesky-mentions  ── agent token ──→
  Postiz + Temporal (docker, :4007 LAN) ←─ POSTIZ_API_KEY ─┘
```

## Commands

```bash
make test vet      # gate before push: go test ./... + go vet ./...
make build         # all binaries → bin/
make generate      # sqlc generate (output is committed)
make spark-bins    # skill CLIs + promo-tribute/promo-restyle for linux/arm64
make deploy-promo-api | deploy-postiz | deploy-spark
```

## Layout

```
cmd/promo-api          server: API + approval bot + publisher + retention (subcommands: serve, check, publish-once, sync-once)
cmd/promo              agent/admin CLI over the API
cmd/{store-reviews,store-metrics,youtube-comments,reddit-monitor,bluesky-mentions}   read-only monitors, JSON arrays to stdout
cmd/promo-tribute      drains the tribute queue on the Spark (ComfyUI), leaves drafts
cmd/promo-restyle      one photo → styled variants on ComfyUI (files + JSON only)
internal/db            migrations (golang-migrate, embedded), sqlc queries + generated code
internal/core          domain: rules.go (limits, duplicates, slots), service.go (lifecycle, inbox)
internal/api           HTTP handlers, token roles
internal/approvals     Telegram approval bot (commands.go is pure and tested)
internal/publisher     approved → Postiz, Postiz state → published/failed
internal/restyle       ComfyUI restyle workflow and style blocks
internal/postiz, telegram, client   thin HTTP clients
internal/monitor/*     API clients behind the monitor CLIs
skills/*/SKILL.md      OpenClaw skills        workspace/   agent workspace files
deploy/promo-api       JODA compose           deploy/postiz  Postiz compose (host-agnostic)
deploy/spark           OpenClaw config example, env templates, units, cron prompts, scripts
projects.yaml          project seed (promo projects import)
```

## Invariants — do not weaken

- **The agent cannot publish.** Enforced by token role in `internal/api`, not by
  prompts: the agent token gets 403 on approve/edit/reject/published and on
  project writes. The admin token, the approval bot token and the Postiz API key
  must never be deployed to the Spark.
- **Approval binds to content.** `posts.revision` increments on every edit;
  approve requires the revision the approver saw (buttons carry it, text commands
  use `notified_revision`). Never approve "whatever is current".
- The approval bot answers only allowlisted Telegram user ids in private chats.
- Agent-created content that breaks a measurable rule is refused with 422 and
  every problem listed; humans editing drafts get warnings instead for duplicates.
- Reddit mention text/author is blanked after `PROMO_REDDIT_RETENTION` (policy).

## Conventions

- Timestamps are TEXT `2006-01-02T15:04:05Z` (UTC, whole seconds) so SQL string comparison is chronological; use `db.FormatTime`/`db.ParseTime`.
- Schema changes: new numbered migration pair in `internal/db/migrations`, then `make generate`.
- sqlc nullable filters use `CAST(sqlc.narg('x') AS TEXT)` so the parameter gets a real type.
- `core` returns `*ValidationError` (422), wraps `ErrNotFound` (404) and `ErrConflict` (409).
- Single DB connection (`SetMaxOpenConns(1)`): inside `inTx` use only the `q` passed in, never `s.q`, or it deadlocks.
- Monitor CLIs print `[]`, never `null`; a source without credentials is skipped
  with exit 0; secrets come from env only (never flags).
- Odchylka od lang.md: bot UI text is Czech; skills and workspace files (read by the agent) are English.

## Kam dál

Gotchas (Telegram, Postiz, JODA, Spark, OpenClaw): `docs/gotchas.md`. Deploy and operations: `docs/RUNBOOK.md`.
