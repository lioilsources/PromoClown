# PromoClown

OpenClaw agent na DGX Sparku, který zná Ol1novy publikované aplikace, navrhuje
promo posty, publikuje je přes Postiz **až po schválení konkrétního textu
v Telegramu** a hlásí recenze, komentáře a zmínky. Implementace plánu
[`Prompts/00-OPENCLAW_PROMO_PLAN.md`](Prompts/00-OPENCLAW_PROMO_PLAN.md);
nasazení krok za krokem je v [`docs/RUNBOOK.md`](docs/RUNBOOK.md).

## Jak to funguje

```
                              Telegram
            ┌────────────────────┴─────────────────────┐
     agent bot (OpenClaw)                    approval bot (promo-api)
     konverzace, weekly plan,                náhledy draftů s médiem,
     digest, hlášení z heartbeatu            ok / edit / no, stav publikace
            │                                          │
 Spark ─────┼────────────────────────     JODA ────────┼───────────────────────
  OpenClaw gateway 127.0.0.1:18789          promo-api 192.168.88.88:8094
   model litellm/openclaw-default            ├─ SQLite: projects, posts, mentions, reviews
     → gateway :8080 → LiteLLM               ├─ approval bot (admin role, bez LLM)
     → vLLM Qwen3.6-35B-A3B-NVFP4            ├─ publisher: approved → Postiz
   skills (Go CLI, agent token) ───────────→ │  (každých 5 min a hned po „ok")
     promo · promo-ingest · store-reviews    └─ sync: stav z Postizu → published/failed
     youtube-comments · reddit-monitor                 │ Postiz API key jen zde
     bluesky-mentions                                  ▼
   heartbeat 15 min · weekly-plan · daily-digest   Postiz + Temporal (docker :4007)
                                                   social.ol1n.com (CF Tunnel + Access)
                                                   → Bluesky, X, YouTube
```

1. **Weekly plan** (po 08:00): agent přečte `PROMO_RULES.md`, `PROJECTS.md` a log
   posledních 30 dní a uloží 3–5 draftů přes `promo posts draft`.
2. promo-api draft zkontroluje (limity platforem, zakázaná tvrzení, duplicity)
   a approval bot ti pošle náhled i s obrázkem/videem.
3. `ok 12` → promo-api vybere slot podle frekvenčních pravidel, publisher do pár
   vteřin založí naplánovaný post v Postizu, bot potvrdí 📅 a po publikaci 🚀 s odkazem.
4. **Heartbeat** (15 min): `promo-ingest` stáhne recenze, YouTube komentáře,
   Bluesky zmínky a Reddit inbox; agent nahlásí jen to, co ještě nehlásil.
5. **Daily digest** (19:00): shrnutí dne, jen když se něco stalo.

## Bezpečnostní model

| Invariant | Jak je vynucený |
|---|---|
| Agent nepublikuje bez schválení | Agent token nemá roli admin: approve/edit/reject/published a zápis projektů vrací 403. Admin token, token approval bota a Postiz API klíč existují jen na JODA. Příkaz `publish` neexistuje. |
| Schvaluje se přesně ten text, který jsi viděl | Každá úprava zvýší `revision`. Tlačítko nese revizi náhledu, `ok 12` použije revizi posledního náhledu; jiná revize je odmítnuta. |
| Boti poslouchají jen tebe | Approval bot: allowlist user ID a jen privátní chat. OpenClaw: `dmPolicy: allowlist`. |
| Gateway není z LAN ani z internetu | `gateway.bind: loopback`; UI přes `ssh -L 18789:127.0.0.1:18789 spark`. |
| Agent spouští jen svoje nástroje | `tools.exec.mode: allowlist` + allowlist šesti binárek; web a browser nástroje zakázané. |
| Jen read-only klíče na Sparku | Monitory pouze čtou; žádné zapisovací klíče ke storům, Cloudflare ani gitu. |
| Obsah dle pravidel | promo-api odmítne draft agenta nad limit platformy, se zakázaným tvrzením nebo s duplicitou z 30 dní; slot volí sám. |

## Komponenty

| Binárka | Kde | Co dělá |
|---|---|---|
| `promo-api` | JODA (docker) | SQLite + HTTP API, approval bot, publisher, sync, retence Reddit dat |
| `promo` | Spark (skill), Mac (admin) | projekty, assety, drafty, log, inbox, digest |
| `store-reviews` | Spark | recenze App Store Connect a Google Play |
| `youtube-comments` | Spark | komentáře na kanálu (API key) |
| `bluesky-mentions` | Spark | zmínky, odpovědi a citace (app password) |
| `reddit-monitor` | Spark | hledání v subredditech + inbox (read-only) |
| `promo-ingest` | Spark | spustí monitory a naimportuje výsledky |

## Schvalování v Telegramu

```
ok 12                         schválit, slot vybere pravidlo frekvence
ok 12 at 2026-09-14T10:00     schválit na konkrétní čas (Europe/Prague)
edit 12 nový text             přepsat text → přijde nový náhled
title 12 nový titulek         titulek YouTube videa
no 12 důvod                   zamítnout
retry 12                      znovu zkusit post, který selhal
done 12 url                   Reddit odpověď odeslaná ručně
show 12 · list · help
```

Frekvence: max 1 post na platformu za den, max 1 post projektu na platformu za
týden, publikační okno 09:00–20:00. Když zadáš čas ručně, pravidla jen připomene.

## Vývoj

```bash
make test         # všechny testy (bez sítě)
make build        # bin/
make generate     # sqlc po změně SQL (výstup je v gitu)
make spark-bins   # skill CLI pro linux/arm64
```

Admin CLI z Macu: `PROMO_API_URL=http://192.168.88.88:8094 PROMO_TOKEN=<admin> promo posts list`.

## Odchylky od plánu

| Plán | Implementace | Proč |
|---|---|---|
| Postiz na JODA | Compose nezávislý na hostiteli, doporučeně Spark | JODA má 3,8 GB RAM a plný swap; Postiz ≥ 2.12 potřebuje Temporal + Elasticsearch + 2× Postgres (~3 GB). |
| Jeden Telegram bot; drafty posílá agent | Dva boti; náhledy a schvalování dělá deterministický bot v promo-api | Token bota může mít jen jednoho pollera (409 Conflict); schvalování je mimo proces s LLM. |
| `process-approvals` jako systemd timer | Publisher uvnitř promo-api (5 min + hned po schválení) | Stejně deterministické, jeden kontejner, kratší latence. |
| Stav `published` přes webhook | Polling Postiz API (5 min); `POST /webhooks/postiz` připravený | Postiz webhooky jsou nepodepsané, chodí jen při úspěchu a jen na veřejnou HTTPS URL. |
| `HEARTBEAT.md` v workspace | Checklist se importuje do scratch heartbeat jobu (`openclaw doctor --fix`) | OpenClaw 2026.9 soubor za běhu nečte; tichá odpověď je `NO_REPLY`. |
| `--tool-call-parser hermes` | `qwen3_xml` | Qwen3.6 volá tooly v XML formátu (NVIDIA recept pro Spark). |
| Profil v AiStack controller-manageru | Rezidentní `docker-compose.agent.yaml` (větev `feat/qwen36-agent`) | Controller při aktivaci jiného modelu shazuje předchozí. |
| LiteLLM `127.0.0.1:4000` | AiStack gateway `127.0.0.1:8080/v1` | Port LiteLLM není publikovaný na host. |
| `refresh-projects` jako OpenClaw cron | systemd user timer | Deterministické, nepotřebuje model. |
| Bluesky zmínky bez skillu | + `bluesky-mentions` | Definition of done 3 počítá se zmínkou na Bluesky. |
| — | Retence Reddit obsahu 48 h | Reddit Data API policy; přístup k API navíc vyžaduje schválení. |
| Heartbeat bez omezení | Aktivní 07:30–22:30 | V noci nebudí; upravíš v `openclaw.json`. |
