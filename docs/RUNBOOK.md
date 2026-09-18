# RUNBOOK — nasazení PromoClown

Pořadí odpovídá plánu (§9) s úpravami z README („Odchylky od plánu").
✋ = ruční krok (účty, tokeny, klikání v UI). Prefix `mac$`, `spark$`, `joda$`
říká, kde příkaz běží.

## 0. Rozhodnutí před nasazením

| Otázka | Zjištěno 2026-09-11 | Doporučení |
|---|---|---|
| Kde poběží Postiz | JODA: 3,8 GB RAM, swap 3,7/3,8 GB plný, 2 CPU | Spark (`POSTIZ_BIND=192.168.88.66`) |
| Paměť pro agent model (~36 GB unified) | Spark: dostupných 17–37 GB podle zátěže | Před startem uvolnit paměť (viz A1) |
| Reddit API | Nové přístupy vyžadují schválení | Požádat hned; bez něj `reddit-monitor` jen přeskočí |

## Fáze E — účty a tokeny ✋ (začni hned, běží paralelně)

1. **Telegram**
   - @BotFather `/newbot` dvakrát: **agent bot** (např. `ol1n_promo_bot`) a **approval bot** (např. `ol1n_promo_approve_bot`). U obou `/setjoingroups` → Disable.
   - @userinfobot → tvoje numerické user ID.
   - Oběma botům pošli `/start` (bot nemůže začít konverzaci sám).
2. **Tajné tokeny** → password manager:
   ```bash
   mac$ for n in PROMO_AGENT_TOKEN PROMO_ADMIN_TOKEN OPENCLAW_GATEWAY_TOKEN; do echo "$n=$(openssl rand -hex 32)"; done
   ```
3. **X** (developer.x.com): app → User authentication settings → OAuth 1.0a, **Read and Write**, Type of App **Native App**, callback `https://social.ol1n.com/integrations/social/x`, Website URL `https://t.me/tsumikimanga_bot`. Zkopíruj Consumer API Key + Secret → `X_API_KEY` / `X_API_SECRET` v `deploy/postiz/.env`.
   **Pozor:** od února 2026 free tier pro nové vývojáře není, default je pay-per-use — post bez odkazu $0,015, **s odkazem $0,20**. Proto šablona tributů odkaz nenese; ten patří do profilu účtu.
4. **Google Cloud** (projekt např. `promoclown`):
   - zapni **YouTube Data API v3**;
   - OAuth consent screen: External, Testing, sebe přidej jako test usera;
   - Credentials → OAuth client ID → *Web application*, redirect `https://social.ol1n.com/integrations/social/youtube` (pro Postiz);
   - Credentials → API key omezený na YouTube Data API v3 (pro `youtube-comments`); ID kanálu `UC…` najdeš na youtube.com/account_advanced.
5. **Bluesky**: Settings → Privacy and security → App passwords → `postiz` a zvlášť `promoclown-monitor` (jde revokovat samostatně).
6. **Reddit**: požádej o přístup k Data API (Responsible Builder Policy, odkaz v „Reddit Data API Wiki" na support.reddithelp.com). Popis: osobní nekomerční read-only monitoring pár subredditů a vlastního inboxu, desítky requestů denně. Po schválení reddit.com/prefs/apps → *script* app; účet bez 2FA.
7. **App Store Connect**: Users and Access → Integrations → App Store Connect API → Team Keys → role **Customer Support** → stáhni `.p8` (jde jen jednou), poznamenej Key ID a Issuer ID. Kirian má app id `6774868017`.
8. **Google Play**: v Google Cloud zapni *Google Play Android Developer API*, vytvoř service account + JSON klíč. Play Console → Users and permissions → Invite → e-mail service accountu → u aplikace oprávnění *Reply to reviews* (API recenzí ho vyžaduje; klíč se používá jen ke čtení). Kirian je na Androidu v closed testing, recenze zatím nebudou.
9. **TikTok** (v2): developers.tiktok.com → app → žádost o Content Posting API (audit trvá týdny).
10. **Cloudflare tunel pro Postiz** — na JODA, kde je `~/.cloudflared/cert.pem`:
    ```bash
    joda$ cloudflared tunnel create postiz              # vypíše UUID, zapíše ~/.cloudflared/<UUID>.json
    joda$ cloudflared tunnel route dns a26613b2-38d2-4fe6-b6b9-268b54babf9b social.ol1n.com
    ```
    Hotovo 2026-09-18: tunel `postiz` = `a26613b2-38d2-4fe6-b6b9-268b54babf9b`,
    veřejné jméno **`social.ol1n.com`**.

    **Past 1 — jméno tunelu prohraje s lokálním configem.** `route dns postiz …`
    založilo CNAME na `media_network` (46b3669e), protože `~/.cloudflared/config.yml`
    na JODĚ určuje default tunel. Hostname pak odpovídá prázdné 404 od cloudflared.
    Routuj vždy **UUID**, a výsledek ověř requestem, ne logem.

    **Past 2 — starý CNAME nejde přepsat.** `postiz.ol1n.com` drží tunel
    `media_network` a `route dns` ho nepřepíše ani s `-f` („already configured").
    Token v `~/.cloudflare/api.env` na Macu má Access práva, DNS ne, takže se to
    musí klikat v dashboardu. Proto vzniklo nové jméno `social.ol1n.com` —
    to `cloudflared` založit umí sám.

    Access aplikace jsou založené přes API: `social.ol1n.com` s policy *owner-sso*
    (allow, tři e-maily) a `social.ol1n.com/uploads` s policy *bypass everyone* —
    Postiz si při publikaci stahuje vlastní média přes veřejnou URL a za Access
    by dostal login stránku. Policy jsou **inline, ne sdílené**: nové aplikaci
    se nedá předat id policy jiné aplikace (`policy … not found`), musí se poslat
    celý objekt. Ručně: Zero Trust → Access → Applications.

## Fáze A — Spark: model, OpenClaw, Telegram

### A1. vLLM `qwen36-agent` (Qwen3.6-35B-A3B-NVFP4)

AiStack na Sparku běží na větvi `feat/translate-memory-profile` (s nepushnutým commitem), ne na `main`. Proto se změny aplikují patchem; lokální větev `feat/qwen36-agent` v AiStacku je připravená na PR do `main` (navíc Makefile targety a CLAUDE.md).

```bash
mac$   scp deploy/spark/aistack-qwen36-agent.patch spark:/tmp/
spark$ cd ~/deploy/AiStack
spark$ git apply --check /tmp/aistack-qwen36-agent.patch && git apply /tmp/aistack-qwen36-agent.patch
spark$ cat >> .env <<'EOF'

# ── Agent LLM (docker-compose.agent.yaml) — OpenClaw / PromoClown
HF_MODEL_AGENT=nvidia/Qwen3.6-35B-A3B-NVFP4
CACHE_AGENT=/home/ol1n/deploy/AiStack/cache/agent
AGENT_VLLM_VERSION=v0.20.0
AGENT_GPU_MEMORY_UTILIZATION=0.30
EOF
spark$ export PATH="$HOME/dev/audio/envs/tools/bin:$PATH"     # hf CLI (v PATH SSH relace není)
spark$ env $(grep -v '^#' .env | xargs) HF_HUB_DISABLE_XET=1 bash scripts/download_qwen36_agent.sh   # ~23,4 GB, ~80 min
# Bez HF_HUB_DISABLE_XET=1 se velké shardy na Sparku zasekly na 0 B/s (2026-09-11); přes HTTP ~4 MB/s.
```

Paměť: model potřebuje ~36 GB unified paměti a musí běžet trvale (heartbeat).

```bash
spark$ free -g                                                    # "available" ≥ 40 GB
spark$ docker stats --no-stream --format '{{.MemUsage}}\t{{.Name}}' | sort -h | tail
spark$ ps -eo rss,comm --sort=-rss | head -5                      # host procesy (ComfyUI apod.)
```

**Stav 2026-09-14:** `vllm/vllm-openai:v0.20.0` model nenačte
(`KeyError: 'layers.0.mlp.experts.w2_input_scale'` — starý loader nezná NVFP4 MoE
škály), proto je v `.env` `AGENT_VLLM_VERSION=latest` (image 22 GB; pull na Sparku jede
~1 MB/s, počítej s hodinami). Do té doby je LiteLLM route `openclaw-default` **dočasně**
přesměrovaná na `swarm-nano` (Nemotron-3-Nano-30B-A3B, tool calling ověřený) — původní
blok je v `deploy/litellm_config.yaml.bak-openclaw-default`. Přepnutí zpět:

```bash
spark$ cd ~/deploy/AiStack && export ENV="env $(grep -v '^#' .env | xargs)"   # compose bez env vars selže na prázdném CACHE_AGENT
spark$ $ENV docker compose --env-file .env -f deploy/docker-compose.swarm.yaml stop swarm-nano   # uvolní ~38 GB
spark$ $ENV docker compose --env-file .env -f deploy/docker-compose.agent.yaml up -d
spark$ docker logs -f qwen36-agent          # čekej na "Application startup complete" (až ~15 min)
spark$ curl -s localhost:8040/v1/models
spark$ python3 - <<'PY'   # openclaw-default zpět na qwen36-agent
import re,p; p="/home/ol1n/deploy/AiStack/deploy/litellm_config.yaml"; s=open(p).read()
s=s.replace("model: openai/swarm-nano","model: openai/qwen36-agent").replace("http://swarm-nano:8000/v1","http://qwen36-agent:8000/v1"); open(p,"w").write(s)
PY
spark$ docker restart litellm               # načte route openclaw-default (pár vteřin výpadek LLM API)
spark$ openclaw config set models.providers.litellm.models[0].contextWindow 65536 --strict-json   # swarm-nano měl 32768
spark$ curl -s localhost:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{
  "model": "openclaw-default",
  "messages": [{"role": "user", "content": "Jaké je počasí v Praze?"}],
  "tools": [{"type": "function", "function": {"name": "get_weather",
    "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}}}]
}' | jq '.choices[0].message'
```

Očekávání: `tool_calls` s `get_weather` a městem, žádné `<think>` v `content`.
Pokud vLLM v0.20.0 architekturu `Qwen3_5MoeForConditionalGeneration` nenačte, nastav v `.env` `AGENT_VLLM_VERSION=latest` (NVIDIA playbook pro Spark) a zopakuj `up -d`. Nepřidávej `--kv-cache-dtype fp8`, na GB10 generuje šum.

### A2. OpenClaw

Instalátor si nejdřív přečti (invariant: nic neinstalovat bez přečtení kódu). `install-cli.sh` stáhne Node do `~/.openclaw/tools`, nepotřebuje sudo (na rozdíl od `install.sh`).

```bash
spark$ curl -fsSL --proto '=https' --tlsv1.2 https://openclaw.ai/install-cli.sh -o /tmp/openclaw-install.sh && less /tmp/openclaw-install.sh
spark$ bash /tmp/openclaw-install.sh
spark$ echo 'export PATH="$HOME/.local/bin:$HOME/.openclaw/bin:$PATH"' >> ~/.bashrc && . ~/.bashrc
spark$ openclaw --version                   # 2026-09-14: 2026.9.4, Node 24.19.0 v ~/.openclaw/tools

mac$   make deploy-spark                    # CLI, skills, workspace, units, šablony (bez tajemství)
spark$ nano ~/.config/promoclown/openclaw.env          # TELEGRAM_AGENT_BOT_TOKEN, TELEGRAM_OWNER_ID (OPENCLAW_GATEWAY_TOKEN vygeneruje deploy)

# Config se NEKOPÍRUJE ze šablony: `openclaw setup --baseline` založí soubor a pak se
# doplňuje přes `config patch` (JSON5, rekurzivní merge, validace proti schématu).
# `${VAR}` v patchi je SecretRef — ověřuje se při zápisu, proto musí být env načtené.
spark$ openclaw setup --baseline --workspace ~/.openclaw/workspace   # nekombinovat s --skip-bootstrap; naše workspace soubory nechá
spark$ set -a; . ~/.config/promoclown/openclaw.env; . ~/.config/promoclown/promo.env; set +a
spark$ for p in ~/.config/promoclown/openclaw-patches/*.json5; do openclaw config patch --file "$p" --dry-run && openclaw config patch --file "$p"; done
spark$ openclaw config validate

# gateway install: env musí být načtené (SecretRef) a adresář
# ~/.config/systemd/user/openclaw-gateway.service.d NESMÍ existovat dřív než jednotka
# („Managed service artifacts changed during publication"). deploy-spark ho vytváří,
# proto ho na chvíli odsuň.
spark$ mv ~/.config/systemd/user/openclaw-gateway.service.d /tmp/ocg.d
spark$ openclaw gateway install --port 18789 --runtime node
spark$ mv /tmp/ocg.d ~/.config/systemd/user/openclaw-gateway.service.d
spark$ systemctl --user daemon-reload && systemctl --user restart openclaw-gateway
spark$ systemctl --user cat openclaw-gateway | grep -E 'EnvironmentFile|PATH'   # drop-in jen EnvironmentFile; PATH z jednotky obsahuje ~/.local/bin
spark$ ss -ltnp | grep 18789                            # jen 127.0.0.1:18789
spark$ openclaw doctor                                  # bez „PATH missing required dirs"
```

Rychlý test bez Telegramu (embedded běh, volá nástroje):
`spark$ openclaw agent --local -m "Vypiš soubory ve svém workspace a stručně řekni, co dělá HEARTBEAT.md."`

Linger je na Sparku zapnutý (`loginctl show-user ol1n -p Linger` → `yes`), služba přežije odhlášení.

Test: `mac$ ssh -N -L 18789:127.0.0.1:18789 spark`, otevři http://127.0.0.1:18789 (token = `OPENCLAW_GATEWAY_TOKEN`) a napiš „vypiš soubory ve workspace" — model musí zavolat nástroj.

### A3. Telegram kanál

Kanál je v `openclaw.json` (`dmPolicy: allowlist`, tvoje ID), pairing není potřeba. Napiš agent botovi „ahoj". Test cronu:

```bash
spark$ m=$(date -d '+2 min' +%M); h=$(date -d '+2 min' +%H)
spark$ openclaw cron add --name hello-test --cron "$m $h * * *" --tz Europe/Prague --exact \
         --session isolated --message "Pošli mi jen slovo ahoj." \
         --announce --channel telegram --to "$TELEGRAM_OWNER_ID"
spark$ openclaw cron list --all            # po doručení: openclaw cron remove <id>
```

**Hotovo A:** agent odpovídá v Telegramu, používá lokální model, `mac$ nmap -p 18789 192.168.88.66` hlásí closed.

## Fáze B — JODA: promo-api; Postiz

### B2. promo-api

```bash
mac$  ssh joda 'mkdir -p ~/deploy/PromoClown/deploy/promo-api'
mac$  scp deploy/promo-api/.env.example joda:deploy/PromoClown/deploy/promo-api/.env
joda$ nano ~/deploy/PromoClown/deploy/promo-api/.env
      # PROMO_AGENT_TOKEN, PROMO_ADMIN_TOKEN, TELEGRAM_APPROVAL_BOT_TOKEN,
      # TELEGRAM_ALLOWED_USER_IDS; POSTIZ_* zatím prázdné
mac$  make deploy-promo-api            # rsync, docker compose up --build, promo-api check
```

Projekty a assety:

```bash
mac$ export PROMO_API_URL=http://192.168.88.88:8094 PROMO_TOKEN=<PROMO_ADMIN_TOKEN>
mac$ go run ./cmd/promo projects import projects.yaml
mac$ go run ./cmd/promo projects list

mac$ A=deploy/PromoClown/deploy/promo-api/data/assets
mac$ ssh joda "mkdir -p $A/kirian/screenshots $A/kirian/shorts $A/doggiowars/screenshots"
mac$ scp /Volumes/YOTTA/Dev/Kiran/tyrian_mobile/assets/icon/icon.png joda:$A/kirian/
mac$ scp /Volumes/YOTTA/Dev/Kiran/tyrian_mobile/marketing/play-assets/phone/* joda:$A/kirian/screenshots/
mac$ scp /Volumes/YOTTA/Dev/DoggioFight/screenshot.png /Volumes/YOTTA/Dev/DoggioFight/menu/icon.png joda:$A/doggiowars/
mac$ go run ./cmd/promo projects assets kirian
```

Pro YouTube Shorts dej do `<slug>/shorts/` vertikální mp4 15–30 s (plán: min. 3 na aplikaci).

Test approval bota: napiš mu `list` → „Žádné drafty ke schválení."

Záloha SQLite (běží za provozu, drží 14 kopií) — přidej do crontabu uživatele `oli` (`crontab -e`):

```
30 3 * * * docker exec promo-api promo-api backup && rsync -a --delete /home/oli/deploy/PromoClown/deploy/promo-api/data/backups/ /media/backups/promoclown/
```

### B1. Postiz na Sparku

```bash
mac$   ssh spark 'mkdir -p ~/deploy/PromoClown/deploy/postiz/cloudflared'
mac$   scp joda:.cloudflared/<UUID>.json /tmp/postiz-credentials.json
mac$   scp /tmp/postiz-credentials.json spark:deploy/PromoClown/deploy/postiz/cloudflared/credentials.json && rm /tmp/postiz-credentials.json
mac$   scp deploy/postiz/.env.example spark:deploy/PromoClown/deploy/postiz/.env
mac$   scp deploy/postiz/cloudflared/config.yml.example spark:deploy/PromoClown/deploy/postiz/cloudflared/config.yml
spark$ nano ~/deploy/PromoClown/deploy/postiz/.env                    # secrets (openssl rand), X, YouTube
spark$ nano ~/deploy/PromoClown/deploy/postiz/cloudflared/config.yml  # UUID tunelu
mac$   make deploy-postiz POSTIZ_HOST=spark
spark$ docker compose -f ~/deploy/PromoClown/deploy/postiz/docker-compose.yml ps   # za 2–3 min vše healthy
spark$ docker logs postiz-cloudflared 2>&1 | grep -c "Registered tunnel connection"   # 4
```

Hotovo 2026-09-14 (všechny kontejnery healthy, LAN `http://192.168.88.66:4007`).
`credentials.json` musí být 0644 — cloudflared image běží jako uid 65532 a s 0600
souborem se konektor točí v restartu (`permission denied`); `deploy.sh` to už nastavuje.
Veřejná URL závisí na opravě DNS záznamu (fáze E bod 10).

Pak:

1. https://social.ol1n.com → vytvoř účet. V `.env` nastav `POSTIZ_DISABLE_REGISTRATION=true` a `make deploy-postiz POSTIZ_HOST=spark` (UI je dostupné z LAN).
2. Přidej kanály: **Bluesky** (identifier + app password `postiz`), **X**, **YouTube**.
3. Settings → Developers → Public API → klíč.
4. Na JODA do `deploy/promo-api/.env`:
   ```
   POSTIZ_API_URL=http://192.168.88.66:4007/api/public/v1
   POSTIZ_API_KEY=<klíč>
   ```
   `mac$ make deploy-promo-api` — `promo-api check` musí vypsat připojené kanály.

## Fáze C — skills na Sparku

```bash
mac$   scp AuthKey_XXXX.p8 spark:.config/promoclown/AuthKey.p8
mac$   scp play-service-account.json spark:.config/promoclown/
spark$ chmod 600 ~/.config/promoclown/*
spark$ nano ~/.config/promoclown/promo.env      # PROMO_TOKEN = PROMO_AGENT_TOKEN (nikdy admin), klíče monitorů
mac$   make deploy-spark                          # zapne i PROJECTS.md timer
spark$ set -a; . ~/.config/promoclown/promo.env; set +a
spark$ promo projects list
spark$ promo posts approve 1; echo "exit $? (má být 2: not allowed)"
spark$ promo-ingest && promo reviews pending && promo mentions pending
spark$ ~/.config/promoclown/setup-openclaw.sh    # exec allowlist, heartbeat checklist, weekly-plan + daily-digest
spark$ set -a; . ~/.config/promoclown/openclaw.env; set +a   # cron/approvals příkazy potřebují OPENCLAW_GATEWAY_TOKEN v env
spark$ openclaw skills list
spark$ openclaw cron list --all                  # Heartbeat (main) every 15m, promo weekly-plan, promo daily-digest
```

Hotovo 2026-09-14: allowlist 6 binárek, heartbeat 15 min (07:30–22:30, → Telegram),
checklist z HEARTBEAT.md ve scratch heartbeat jobu (`openclaw cron scratch <id>`),
cron `promo weekly-plan` (Po 08:00) a `promo daily-digest` (19:00).
`openclaw doctor --fix` zastaví gateway a ne vždy ho nastartuje; skript ho proto před
`cron add` restartuje.

První reálný post (plán §9 bod 5), ručně nebo přes agenta („navrhni jeden Bluesky post pro Kirian"):

```bash
spark$ promo projects assets kirian
spark$ promo posts draft --project kirian --platform bluesky \
         --text "…https://apps.apple.com/us/app/kirian/id6774868017" --media kirian/screenshots/<soubor>.png
```

V approval botovi přijde náhled → `ok <id>` → 📅 naplánováno → v čase slotu 🚀 s odkazem.

## Fáze D — provoz

Cron joby a heartbeat nastavil `setup-openclaw.sh`, PROJECTS.md obnovuje timer ve 03:00. Nech týden zkušebního provozu a sleduj, co agent navrhuje.

### D1. Tribute posty (Tsumiki, X)

Pošleš approval botovi **obrázek** a do popisku **autora**: `@artist`. Jiný projekt
vybereš `#slug` (default je `TRIBUTE_PROJECT`, tedy `tsumiki`). Bot obrázek uloží
mezi assety a zařadí do fronty — žádný model u toho není, funguje to celý den,
i když agent neběží.

Ve **23:00** timer `promo-tribute.timer` na Sparku frontu vyprázdní: pro každý
záznam vybere 4 styly, nechá ComfyUI vyrobit 4 varianty a založí draft na X.
Ráno ho schvaluješ v Telegramu jako cokoli jiného; publikuje ho publisher na JODĚ.

```bash
spark$ systemctl --user list-timers promo-tribute.timer
spark$ promo-tribute run --dry-run          # jen ukáže, jaké styly by padly
spark$ systemctl --user start promo-tribute # spustit frontu hned
spark$ journalctl --user -u promo-tribute -f
mac$   go run ./cmd/promo tributes list
```

Proč 23:00: `rag-schedule.sh` (WorldLibraryProject) v půlnoci ComfyUI shodí, aby se
do paměti vešel agent. Jeden obrázek trvá 66–120 s, čtyři tedy 5–8 minut; fronta
musí doběhnout před půlnocí, jinak zbytek zůstane `queued` na další noc.

**Nastavení, která jsou naměřená, ne odhadnutá** (MangaPrompts
`docs/restyle-rollout-results.md`): checkpoint `sd_xl_base_1.0`, InstantID
`ip_weight` 0.6, depth ControlNet drží pózu, InstantID podobu. Juggernaut styl
ignoruje, Animagine ztrácí identitu, `ip_weight` 0.8 přebije styl na postavě —
proto k nim odsud nevede cesta. `flux-schnell` je txt2img bez řízení pózy,
na tohle se nehodí vůbec.

Frekvence: `tsumiki` má v `projects.yaml` `daily_cap: 3` a `min_days_between: 0`,
takže tributů může být víc denně. Pravidla se počítají **per účet**, takže
Kirianovi na jeho vlastním X účtu nic neblokují.

## Ověření (Definition of done, plán §7)

| # | Test | Očekávání |
|---|---|---|
| 1 | `spark$ openclaw cron list --all` → `openclaw cron run <weekly-plan id> --wait` | Náhledy draftů v approval botovi, shrnutí v agent botovi; opakovaný text API odmítne (exit 2) |
| 2 | V approval botovi `ok <id>` | Do pár vteřin 📅 a post v kalendáři Postizu; po publikaci 🚀 do 5 min (`PROMO_SYNC_INTERVAL`), `promo posts show <id>` → `published` |
| 3 | Komentář pod vlastním YouTube videem a zmínka tvého účtu na Bluesky | Do 15 min zpráva agent bota s odkazem (heartbeat běží 07:30–22:30) |
| 4 | Nová recenze v App Store / Play | V daily digestu v 19:00 nebo v heartbeatu (Play vrací jen recenze z posledních 7 dní) |
| 5 | `mac$ nmap -p 18789 192.168.88.66`, `spark$ ss -ltnp \| grep 18789` | closed; naslouchá jen 127.0.0.1 |
| 6 | Agentovi: „publikuj to hned" | Odmítne a odkáže na approval bota; i pokus `promo posts approve` vrátí „not allowed" |

## Údržba

- **Logy:** `joda$ docker logs -f promo-api` · `spark$ journalctl --user -u openclaw-gateway -f` · `openclaw cron runs <id>` · `docker compose -f ~/deploy/PromoClown/deploy/postiz/docker-compose.yml logs -f postiz`
- **Změna skills/workspace/promptů:** uprav repo → `make deploy-spark`. Cron prompty jsou v jobech: `openclaw cron edit <id>`, nebo job smaž a spusť `setup-openclaw.sh` znovu.
- **Projekty:** uprav `projects.yaml` → `promo projects import projects.yaml` (admin token). Aplikaci po vydání přepni na `active` a doplň store odkaz.
- **Záloha Sparku:** `~/.openclaw/` a `~/.config/promoclown/` (restic/rsync na JODA `/media/backups`).
- **Rotace tokenů:** nový `PROMO_AGENT_TOKEN` do obou `.env`, `make deploy-promo-api`, `make deploy-spark`.

| Příznak | Příčina / řešení |
|---|---|
| Approval bot loguje „another process is polling" | Stejný token používá i jiný proces (např. OpenClaw) — musí to být dva různí boti |
| Skill chybí v `openclaw skills list` | Binárka není na PATH gateway (drop-in) nebo chybí env z `requires.env` |
| Agent píše, že příkaz nesmí spustit | Binárka není v `openclaw approvals get` → `setup-openclaw.sh` |
| `promo-tribute` hlásí `No face detected` | InstantID nenašel v předloze obličej — pošli jiný obrázek, nebo tribute zruš (`promo tributes cancel <id>`) |
| Fronta zůstala `queued` | Timer neběžel, nebo doběhl čas okna; `systemctl --user start promo-tribute` ručně, ale jen dokud ComfyUI běží (do půlnoci) |
| Tribute visí ve `running` | Proces spadl mezi claimem a zápisem; `promo tributes retry <id>` |
| Post `failed` s chybou uploadu médií | Access bypass pro `/uploads/*` chybí; `retry <id>` |
| `no enabled bluesky channel in Postiz` | Kanál v Postizu odpojený nebo vypnutý |
| `reddit-monitor`: 401/403 | Přístup k Reddit API ještě není schválený |
| Heartbeat mlčí | Mimo 07:30–22:30, nebo není co hlásit (`NO_REPLY`); `openclaw system heartbeat last` |
