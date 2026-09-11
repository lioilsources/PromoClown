# OPENCLAW_PROMO_PLAN.md

Handoff dokument pro Claude Code. Cíl: OpenClaw agent na DGX Sparku, který
udržuje seznam publikovaných aplikací, navrhuje promo obsah, publikuje přes
Postiz **výhradně po schválení v Telegramu** a hlásí komentáře/recenze/zmínky.

## 0. Rozhodnutí (fixní)

| Oblast | Rozhodnutí |
|---|---|
| Autonomie | Human-in-the-loop. Agent nikdy nepublikuje bez explicitního schválení konkrétního textu/media v Telegramu. |
| Model | `nvidia/Qwen3.6-35B-A3B-NVFP4` na vLLM (Spark), přes LiteLLM gateway |
| Platformy v1 | YouTube (publikace), Bluesky (publikace), X (publikace), Reddit (jen monitoring + ručně schválené odpovědi), App Store Connect + Google Play (monitoring recenzí) |
| Platformy v2 | TikTok (po auditu Content Posting API), Twitch (oznámení streamů, VOD → Shorts), Product Hunt |
| Rozhraní | Telegram bot, jediný kanál pro návrhy, schvalování a notifikace |
| Publikační vrstva | Postiz self-hosted na JODA |
| Stav/paměť | Go CLI `promo` nad SQLite (skill pro OpenClaw) + markdown workspace soubory |

## 1. Topologie

```
Mac Mini (control)  ──ssh/tailscale──▶  Spark
                                        ├── vLLM  :8000   Qwen3.6-35B-A3B-NVFP4
                                        ├── LiteLLM :4000 (existující gateway)
                                        ├── OpenClaw Gateway :18789 (bind 127.0.0.1 only)
                                        │     workspace ~/.openclaw/workspace/
                                        │     skills: promo, postiz, reddit-monitor, store-reviews
                                        │     cron: weekly-plan, daily-digest
                                        │     heartbeat: check-inbox (15 min)
                                        └── Telegram (outbound only, long polling)

JODA (Docker Compose)
  ├── postiz + postgres + redis        → postiz.ol1n.com (Cloudflare Tunnel + Access)
  └── promo-api (Go, SQLite)           → promo.ol1n.com (jen webhooky, Cloudflare Access + secret)
```

Bezpečnostní invarianty:
- OpenClaw gateway **není** za Cloudflare Tunnelem. Přístup k web UI jen přes SSH tunel (`ssh -L 18789:127.0.0.1:18789 spark`) nebo Tailscale.
- Agent nemá přístup k App Store Connect / Play Console zápisu, k Cloudflare API ani ke git push. Jen read-only klíče.
- Telegram bot přijímá zprávy pouze od tvého user ID (allowlist v OpenClaw config).
- Skilly pouze vlastní nebo auditované; žádné `clawhub install` bez přečtení kódu.

## 2. Fáze A – Spark: model + OpenClaw (1 den)

### A1. vLLM s Qwen3.6-35B-A3B-NVFP4
- Stáhnout image: `docker pull vllm/vllm-openai:nightly-aarch64` (ověřit aktuální tag v NVIDIA playbooku `NVIDIA/dgx-spark-playbooks/nvidia/openclaw`).
- Přidat do AiStack controller-manageru jako další profil (`qwen36-agent`), nebo ručně:

```bash
docker run -d --name vllm-qwen36 --gpus all --ipc=host -p 8000:8000 \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai:nightly-aarch64 \
  --model nvidia/Qwen3.6-35B-A3B-NVFP4 \
  --max-model-len 65536 \
  --enable-auto-tool-choice --tool-call-parser hermes \
  --served-model-name qwen36-agent
```
- Ověřit: `curl localhost:8000/v1/models`.
- Pozn.: pro OpenClaw posílat `enable_thinking: false` (přes LiteLLM `extra_body`), thinking bloky matou tool calling.
- Zaregistrovat v LiteLLM configu jako `openclaw-default` → `openai/qwen36-agent`, `api_base: http://localhost:8000/v1`.

### A2. Instalace OpenClaw
- Postup dle playbooku (Node.js LTS, instalátor, onboarding wizard). Uložit vypsanou dashboard URL + token do password manageru.
- Config `~/.openclaw/openclaw.json` (klíče ověřit proti verzi, kterou nainstaluješ – `openclaw config --help`):

```json
{
  "models": {
    "mode": "merge",
    "providers": {
      "litellm": {
        "baseUrl": "http://127.0.0.1:4000/v1",
        "apiKey": "placeholder",
        "models": [{ "id": "openclaw-default", "contextWindow": 65536 }]
      }
    }
  },
  "gateway": { "bind": "127.0.0.1", "port": 18789 }
}
```
- Systemd user unit `openclaw-gateway.service` (Restart=always, `loginctl enable-linger`).
- Test: chat v TUI/web UI, ověřit že model volá tooly (např. „vypiš soubory v workspace").

### A3. Telegram kanál
- BotFather → token. Bot pojmenovat např. `ol1n_promo_bot`.
- `openclaw channels add telegram`, provést pairing s tvým user ID, nastavit allowlist.
- Test: cron job „za 2 minuty mi pošli ahoj" → přijde do Telegramu.

Definition of done A: agent odpovídá v Telegramu, používá lokální model, gateway není dostupná z LAN.

## 3. Fáze B – JODA: Postiz + promo-api (1 den)

### B1. Postiz
- `docker-compose.postiz.yml` (postiz, postgres 16, redis). Env: `MAIN_URL=https://postiz.ol1n.com`, `FRONTEND_URL`, `NEXT_PUBLIC_BACKEND_URL`, `JWT_SECRET`, storage local.
- Caddy route `postiz.ol1n.com` → postiz:5000, Cloudflare Access policy (jen tvůj e-mail). OAuth callbacky z YouTube/X potřebují veřejný hostname – Access musí mít bypass pro `/integrations/social/*` callback cesty (ověřit v Postiz docs).
- Připojit integrace: YouTube (Google Cloud projekt, OAuth consent – interní/testing režim stačí pro vlastní účet), X (developer free tier, write scope), Bluesky (app password).
- Vygenerovat Postiz API key → `POSTIZ_API_KEY`.

### B2. promo-api (Go)
Repo `lioilsources/promo-api` (nebo jako `services/promo` v AiStack). SQLite + sqlc + golang-migrate, port 8094.

Tabulky:
- `projects(id, name, slug, store_ios_url, store_android_url, tagline, audience, tags, assets_dir, status)`
- `posts(id, project_id, platform, kind, text, media_path, status[draft|approved|scheduled|published|rejected], postiz_post_id, scheduled_at, published_at, telegram_msg_id)`
- `mentions(id, platform, external_id, url, author, text, project_id, seen_at, handled)`
- `reviews(id, store, app_id, external_id, rating, text, version, seen_at, handled)`

HTTP: `GET/POST /projects`, `GET/POST/PATCH /posts`, `GET /posts?status=draft`, `POST /mentions`, `GET /mentions?handled=false`, `POST /webhooks/postiz` (stav publikace).

Ochrana: header `X-Promo-Token`, dostupné jen z LAN + přes Cloudflare Access pro webhooky.

## 4. Fáze C – Skilly pro OpenClaw (2 dny)

Všechny vlastní skilly = Go CLI binárky (cross-compile pro linux/arm64) + `SKILL.md`. Umístit do `~/.openclaw/skills/<name>/`.

### C1. `promo` (stav projektů a postů) – volá promo-api
```
promo projects list
promo projects show <slug>
promo posts draft --project <slug> --platform bluesky --text "..." [--media path]
promo posts list --status draft|approved|scheduled
promo posts approve <id>      # volá jen approval handler, ne agent
promo posts log               # co bylo kde a kdy publikováno (agent čte před každým návrhem)
promo mentions pending
promo reviews pending
```
SKILL.md pravidlo: *Agent smí volat pouze `list/show/draft/log/pending`. `approve` a `publish` jsou zakázané.*

### C2. `publish` (Postiz wrapper) – spouštěn approval handlerem, ne agentem
- Bere `post.id` ve stavu `approved`, nahraje media, vytvoří post v Postizu (`postiz posts:create`), uloží `postiz_post_id`, přepne na `scheduled`.
- Volá se z OpenClaw pouze z jasně oddělené cron úlohy `process-approvals`, která nečte model – čistý skript. (Klidně obyčejný systemd timer mimo OpenClaw; jednodušší a bezpečnější.)

### C3. `reddit-monitor` (read-only)
- Reddit script app, OAuth `read` + `privatemessages`. CLI: `reddit-monitor search --sub FlutterDev,indiegames,SideProject,iosapps,androidapps --keywords "..."`, `reddit-monitor inbox`.
- Výstup JSON → agent uloží zajímavé do `promo mentions`.

### C4. `store-reviews` (read-only)
- App Store Connect API key (role Customer Support stačí na čtení recenzí), Google Play service account (View app information). CLI: `store-reviews fetch --since <ts>` → `promo reviews`.

### C5. `youtube-comments` (read-only)
- Přes Postiz nejde; YouTube Data API `commentThreads.list` na vlastní videa (levné kvótově). CLI → `promo mentions`.

## 5. Fáze D – Workspace, pravidla, joby (1 den)

### D1. Workspace soubory `~/.openclaw/workspace/`
- `IDENTITY.md` – kdo agent je: promo asistent Ol1na, indie dev z ČR, apps v Go/Flutter; nikdy netvrdí, že je člověk; nikdy nepublikuje.
- `PROJECTS.md` – generovaný z promo-api (`promo projects list --md`), cron ho refreshuje. Per projekt: název, one-liner, odkazy, cílovka, 3 hooky, co je zakázané slibovat.
- `PROMO_RULES.md`:
  - Tón: lidský, konkrétní, bez marketingových frází, bez emoji spamu, EN default, CZ pro cz subreddity.
  - Frekvence: max 1 post/platforma/den, max 1 post/projekt/týden na stejné platformě, Reddit odpověď jen tam, kde je otázka relevantní.
  - Vždy přiložit odkaz na store + 1 vizuál.
  - Před návrhem vždy `promo posts log` – nikdy neopakovat text z posledních 30 dní.
- `HEARTBEAT.md` – checklist pro heartbeat: `promo mentions pending`, `promo reviews pending`, `reddit-monitor inbox`; pokud nic nového → ticho (žádné „nic nového" zprávy).

### D2. Cron joby (`openclaw cron add`)
| Job | Kdy | Co |
|---|---|---|
| `weekly-plan` | Po 08:00 | Přečíst PROJECTS.md + log, navrhnout 3–5 draftů (Bluesky/X/YT popisek), uložit `promo posts draft`, poslat do Telegramu se shrnutím a ID |
| `daily-digest` | denně 19:00 | Nové mentions/reviews za den + stav scheduled postů; jen pokud něco je |
| `refresh-projects` | denně 03:00 | Regenerovat PROJECTS.md z promo-api |
| `process-approvals` | každých 5 min (systemd timer, bez modelu) | `publish` pro approved posty |

Heartbeat: každých 15 min podle HEARTBEAT.md, doručení do Telegramu jen při nálezu.

### D3. Schvalovací flow v Telegramu
- Agent posílá draft ve formátu: `#<post_id> [platform] [project]` + text + náhled media.
- Ty odpovíš `ok 12` / `edit 12 <nový text>` / `no 12` / `ok 12 at 2026-09-14T10:00`.
- Malý handler (součást `promo` skillu, `promo telegram-listen`, nebo Telegram command přes OpenClaw) mapuje odpověď → `promo posts approve/reject/update`. Agent (LLM) v této cestě nefiguruje – schválení je deterministické.

## 6. Fáze E – Obsah a účty (paralelně, začít hned)

- Google Cloud projekt: YouTube Data API v3 zapnuto, OAuth consent screen. Pro vlastní kanál stačí „Testing" režim (bez verifikace).
- X developer účet (free tier), Bluesky app password, Reddit script app (read-only), App Store Connect API key, Play service account, Telegram bot.
- Assety per projekt: `assets/<slug>/{icon.png, screenshots/, shorts/}` – min. 3 vertikální videa 15–30 s per publikovaná app (Kiran, ImmunoRun, DuolingoCards…; gameplay capture nebo Tsumiki pipeline).
- TikTok: založit developer účet a podat žádost o Content Posting API teď – audit trvá týdny, do v1 nespadá.

## 7. Ověření / Definition of done

1. `weekly-plan` vytvoří drafty, dorazí do Telegramu, text respektuje PROMO_RULES (žádný opakovaný text, správné odkazy).
2. `ok <id>` → do 5 min je post naplánovaný v Postizu, viditelný v kalendáři, po publikaci stav `published` přes webhook.
3. Testovací komentář pod vlastním YT videem a testovací zmínka na Bluesky se objeví v Telegramu do 15 min s odkazem.
4. Nová recenze na App Store / Play dorazí do digestu.
5. `nmap` z LAN: port 18789 na Sparku není dostupný; gateway odpovídá jen na 127.0.0.1.
6. Agent při pokusu „publikuj to hned" odmítne a odkáže na schvalovací flow.

## 8. Rizika a poznámky

- Klíče OpenClaw configu a CLI se mezi verzemi mění – vždy ověřit `openclaw --help` po instalaci, nedržet se slepě tohoto dokumentu.
- Qwen3.6-35B-A3B: pokud bude halucinovat tool argumenty, snížit teplotu na 0.2 a zúžit počet aktivních skillů (OpenClaw načítá skilly on-demand, ale SKILL.md popisy jsou v kontextu).
- Reddit: i schválené odpovědi drž pod ~1 promo odpověď/týden/subreddit; účet používej i ručně na normální interakce.
- X free tier limit na psaní (~1 500 postů/měsíc) je pro tebe irelevantní, ale read endpointy jsou placené – proto X monitoring není ve v1.
- Zálohovat `~/.openclaw/` a promo-api SQLite na JODA (restic/rsync cron).

## 9. Pořadí práce pro Claude Code

1. A1–A3 na Sparku (model, OpenClaw, Telegram).
2. B2 promo-api (Go) – lze psát paralelně na Macu, nasadit na JODA.
3. B1 Postiz + OAuth integrace.
4. C1 `promo` skill → první ruční draft flow end-to-end.
5. C2 `publish` + `process-approvals` timer → první reálný post (Bluesky).
6. C3–C5 monitoring skilly + heartbeat.
7. D1–D2 workspace, pravidla, cron; týden zkušebního provozu.
