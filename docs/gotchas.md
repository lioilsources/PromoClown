# Gotchas

Moved from CLAUDE.md. Project traps that are not visible in the code.

- A Telegram bot token has exactly one poller. The OpenClaw bot and the approval
  bot must be two different bots.
- Postiz: `Authorization: <key>` without Bearer; webhooks are unsigned, success-only
  and need a public HTTPS URL, so state is polled (`PROMO_SYNC_INTERVAL`);
  `POST /posts` returns no group; YouTube needs exactly one mp4; media is fetched
  back through `POSTIZ_URL/uploads`, so Cloudflare Access needs a bypass there.
- JODA: snap Docker only sees `$HOME` and `/media`; published ports bypass ufw;
  no passwordless sudo; swap is already full — do not put Postiz there.
- Spark: unified memory; the agent model needs ~36 GB. The AiStack controller
  stops the previous model on activate, so `qwen36-agent` runs outside it.
- OpenClaw (2026.9): HEARTBEAT.md is imported into the heartbeat job scratch by
  `openclaw doctor --fix`; silent reply token is `NO_REPLY`; TOOLS.md is retired
  (tools live in AGENTS.md); exec is restricted by `openclaw approvals allowlist`.
