# Plán: další zdroje — Discord, Fediverse, regionální komunity

Stav k 2026-09-14. Navazuje na `README.md` (co běží) a `docs/RUNBOOK.md`.
Ověřeno = přečteno v kódu Postizu (lokální klon) nebo v oficiální dokumentaci.
**Neověřeno** = z obecné znalosti; před psaním kódu se musí ověřit (viz §9 —
research běh se přerušil na limitu session a je potřeba ho zopakovat).

## 1. Dvě otázky, které rozhodují o každé platformě

1. **Vlastní prostor, nebo cizí komunita?** Do vlastního (náš Discord server,
   náš Mastodon účet, náš Telegram kanál) můžeme publikovat automaticky. Do
   cizí komunity (subreddit, cizí Discord, fórum) jen ručně po schválení —
   stejné pravidlo jako u Redditu: agent najde vlákno a navrhne odpověď,
   Ol1n ji pošle sám. Automatizované self-promo tam vede k banu a ničí
   reputaci účtu, na kterém promo stojí.
2. **Existuje API a dovolují ho podmínky?** Bez API = ruční práce, agent
   může nanejvýš připravit text. Se čtecím API = `*-monitor` CLI (JSON →
   `promo mentions import`). S publikačním API v Postizu = zapnout provider.

Odtud tři role, stejné jako dnes: **monitor** (čtení, vlastní Go CLI),
**post** (Postiz), **ruční** (agent připraví, člověk pošle).

## 2. Discord

### Jak funguje (ověřeno: Discord dev docs + Postiz `discord.provider.ts`)

- Discord = **servery** (guilds) → **kanály** (text/forum/vlákna). Žádné
  globální vyhledávání napříč servery; vidíš jen servery, kde jsi členem.
- **Člověk** vstoupí přes invite link (`discord.gg/…`) nebo Server Discovery
  (jen velké veřejné servery).
- **Bot se nikam nepřidá sám.** Přidá ho člověk s právem *Manage Server*
  přes OAuth2 URL (`scope=bot`, `permissions=…`). Admin cizího serveru
  promo bota nepřidá.
- Číst text zpráv smí bot jen se zapnutým privilegovaným intentem
  **MESSAGE_CONTENT** (u botů pod 100 serverů jen přepínač v Developer
  Portalu). Historii kanálu jde číst i bez gateway přes REST
  `GET /channels/{id}/messages`.
- **Self-bot** (skript pod lidským účtem) je proti ToS → ban účtu. Takže
  „bot čte servery, kam jsem se přidal jako člověk" nejde.
- Postiz Discord provider: OAuth `bot identify guilds`, permissions
  `377957124096`, bot token v `DISCORD_BOT_TOKEN_ID`; vypíše kanály serveru
  (`GET /guilds/{id}/channels`), posílá `POST /channels/{id}/messages`,
  umí založit vlákno k příspěvku a `@zmínit` členy. Tj. publikuje jen do
  serverů, kde je bot přidaný.

### Co z toho plyne

| | Vlastní server („Ol1n Games") | Cizí servery (r/shmups, Luanti, Flutter…) |
|---|---|---|
| Monitor | ✅ bot čte `#feedback`, `#support`, `#bugs` → `promo mentions` | ❌ jen ručně jako člověk |
| Post | ✅ Postiz (bot) nebo webhook: release notes, nové video | ❌ ručně, jen do `#self-promo`, kde to pravidla dovolují |
| Hledání nových hráčů | ne — server je pro lidi, kteří hru už mají | ano, ale ručně a střídmě |

Discord je nástroj na **obsluhu vlastní komunity**, ne na akvizici. Akvizice
zůstává na Redditu/X/YouTube.

### Plán integrace (odhad 1 den)

1. ✋ Ol1n: založit server (kanály `#announcements`, `#kirian`, `#doggiowars`,
   `#feedback`, `#support`, `#self-promo-ostatní`), invite link do App Store
   popisku, README, YouTube popisků. V Developer Portalu založit aplikaci +
   bota, zapnout **Message Content Intent**, přidat bota na server
   (Send Messages, Read Message History, Create Public Threads).
2. Postiz: `DISCORD_CLIENT_ID/SECRET/DISCORD_BOT_TOKEN_ID` do
   `deploy/postiz/.env`, připojit kanál v UI. promo-api: platforma
   `discord` v `posts.platform` (migrace), settings `__type: discord` +
   `channel`. Frekvenční pravidlo: max 1 post/den, jen release notes a
   nová videa (ne promo texty jako na X).
3. `discord-monitor` CLI (Go, stdlib): env `DISCORD_BOT_TOKEN`,
   `DISCORD_GUILD_ID`, `DISCORD_CHANNELS` (csv id); `fetch --since 48h` →
   `GET /channels/{id}/messages?after=<snowflake>` (snowflake spočítat
   z času), přeskočit vlastní zprávy a bota, `Mention{platform:"discord",
   kind:"message", external_id: message id, url:
   discord.com/channels/{g}/{c}/{m}, context: kanál}`. Rate limit: číst
   hlavičky `X-RateLimit-Remaining/Reset-After`.
4. `promo-ingest` + HEARTBEAT: přidat řádek; agent v hlášení označí, co
   je bug report (→ Ol1n), co dotaz (→ navrhne odpověď, ale odpovídá Ol1n
   ručně, bot v komunitě nemluví za něj).
5. Cizí servery: ruční seznam v `PROMO_RULES.md` (server, kanál pro promo,
   pravidla, poslední post) — agent připraví text, Ol1n pošle. Max 1×/měsíc
   na server.

## 3. Fediverse — nejlevnější rozšíření dosahu (ověřeno: Postiz providery `mastodon`, `mastodon.custom`, `lemmy`)

**Mastodon** — otevřené API (čtení i psaní), Postiz publikuje (OAuth na
libovolné instanci). Zmínky/odpovědi čte `GET /api/v1/notifications`
(stejný tvar jako Bluesky → `mastodon-mentions` CLI ~0,5 dne). Navíc
veřejné hashtag timeline (`/api/v1/timelines/tag/:tag`, bez přihlášení) =
levný monitor `#shmup #indiegame #luanti #flutterdev`. Regionální dosah přes
výběr instance: `mastodon.social` (globál), `mstdn.jp`/`pawoo.net` (JP,
neověřeno kolik hráčů), `chaos.social`/`mastodon.de` (DE), `fosstodon.org`
(dev). Jeden účet, cross-instance viditelnost přes hashtagy. Riziko ToS: nulové.

**Lemmy** — Reddit-alternativa s otevřeným API (`GET /api/v3/search`,
`/api/v3/post/list`). Postiz publikuje (service URL + jméno + heslo).
Komunity: `lemmy.world/c/games`, `/c/indiegaming`, `feddit.org` (DE),
`sopuli.xyz` (FI), `lemmy.ca`. Normy stejné jako Reddit → zacházet stejně:
`lemmy-monitor search` + ručně schválené odpovědi; oznámení vydání do
`/c/indiegaming` bývá povolené (ověřit sidebar každé komunity). ~0,5 dne.

**Threads** (Meta) — Postiz provider existuje; API pro publikování a čtení
odpovědí vyžaduje Meta app review (neověřeno, jak dlouho). Odložit.

**Telegram** — vlastní kanál + diskusní skupina, viz §3b.

## 3b. Messaging platformy: Telegram, WeChat, LINE / KakaoTalk / WhatsApp

Messaging aplikace mají společný rys: **žádné veřejné vyhledávání obsahu**
a skupiny jsou za pozvánkou. Fungují jako vlastní prostor (kanál, který si
lidé odebírají), ne jako místo, kde se hledají noví hráči.

### Telegram (ověřeno: Bot API, Postiz `telegram.provider.ts`)

Jak funguje:
- Soukromé chaty, **skupiny** (do 200 000 členů), **kanály** (jednosměrné
  vysílání, neomezený počet odběratelů), **boti**. Veřejné skupiny a kanály
  mají `@username` a jdou najít v globálním hledání klienta — podle názvu,
  ne podle obsahu zpráv. Oficiální discovery API neexistuje; katalogy
  (tgstat apod.) jsou třetí strany.
- **Bot** se do skupiny/kanálu nepřidá sám — přidá ho admin. V kanálu musí
  být admin s právem *Post messages*. Ve skupině čte všechny zprávy jen
  s vypnutým privacy mode (`/setprivacy` → Disable) nebo jako admin. Cizí
  skupiny = stejný problém jako u Discordu.
- **Userbot** (vlastní účet přes MTProto, `api_id`/`api_hash`): na rozdíl
  od Discordu Telegram klienty třetích stran povoluje, ale API podmínky
  zakazují spam a hromadné zprávy; čtení veřejných skupin pod vlastním
  účtem je šedá zóna. Kdyby vůbec, tak jen čtení, nikdy psaní. Neověřeno
  do detailu → §9. Ve v1 ne.
- **Postiz provider**: používá vlastní bot token `TELEGRAM_TOKEN`
  (Postiz sám volá `getUpdates` → musí to být **samostatný bot**, token
  smí pollovat jen jeden proces). Bota přidáš jako admina do kanálu,
  Postiz ověří `getChatMember`, posílá text / foto / video / album
  (`sendMessage`, `sendPhoto`, `sendVideo`, `sendMediaGroup`) a umí
  komentář jako reply.

Bot inventář po rozšíření (každý má svého jediného pollera):
`@ol1n_promo_bot` (agent, OpenClaw) · `@ol1n_promo_approve_bot` (promo-api)
· `ol1n_postiz_bot` (Postiz, publikace) · `ol1n_community_bot`
(monitor diskusní skupiny).

Plán (≈ 0,5 dne + založení):
1. ✋ Založit veřejný kanál (např. `@ol1n_games`) a k němu navázanou
   diskusní skupinu (Telegram ji k kanálu připojí — každý post kanálu má
   pod sebou komentáře). Odkaz do App Store popisku, README, YouTube.
2. ✋ BotFather: `ol1n_postiz_bot` (admin kanálu, Post messages) a
   `ol1n_community_bot` (člen diskusní skupiny, privacy mode Disable,
   skupiny povolené — na rozdíl od ostatních dvou).
3. Postiz: `TELEGRAM_TOKEN` do `deploy/postiz/.env`, připojit kanál v UI.
   promo-api: platforma `telegram` (limit 4 096 znaků, u zprávy s médiem
   1 024 znaků caption), settings `__type: telegram`. Pravidlo: jen
   release notes a nová videa, max 1/den; text může být CZ i EN naráz.
4. `telegram-monitor fetch --since 48h`: Bot API `getUpdates` pod
   `ol1n_community_bot` s vlastním offset souborem (stejně jako approval
   bot), přeskočit vlastní boty; `Mention{platform:"telegram",
   kind:"message", external_id: "<chat>/<message_id>", url:
   https://t.me/c/<chat bez -100>/<message_id>, context: název skupiny}`.
   Telegram drží nedoručené updaty 24 h — heartbeat běží po 15 min,
   stačí. Do `promo-ingest` a HEARTBEAT checklistu.
5. Cizí kanály a skupiny: objevování nových hráčů = placené posty
   u adminů nebo Telegram Ads — mimo v1. Ručně jen tam, kde skupina
   promo dovoluje.

Dosah: Telegram je silný v Brazílii, Rusku/CIS, Íránu, Indii, Indonésii,
na Ukrajině; v CZ/SK spíš tech publikum. Tsumiki už na Telegramu jako
Mini App žije — kanál je pro ni logické místo, Kirian/DoggioWars tam
dostanou release notes.

### WeChat — verdikt: **skip** (neověřené jen přesné aktuální podmínky)

Jak funguje: uzavřený ekosystém. **Skupiny** (max 500 členů) jen přes
pozvánku/QR, žádné veřejné hledání, žádní boti — automatizace skupin přes
neoficiální klienty končí banem účtu. Veřejný obsah = **Official
Accounts** (公众号: subscription/service), **Channels** (视频号, video)
a **Mini Programs**.

Proč to pro nás nedává smysl:
1. **Registrace**: Official Account si fyzická osoba založí jen s čínským
   občanským průkazem; zahraniční subjekt jen jako **firma** s obchodní
   registrací a ověřovacím poplatkem (řádově 99 USD, týdny). Jako
   jednotlivec z ČR to nejde. API existuje jen pro publikaci na vlastním
   OA, žádné čtení cizího obsahu.
2. **Trh**: Kirian má IAP → v čínském App Store by potřeboval herní
   licenci (版号), pro zahraničního sólo vývojáře nedosažitelnou; čínští
   hráči by museli mít zahraniční Apple ID. WeChat by vedl lidi tam, kde
   nemůžou koupit.
3. **Monitoring** není možný vůbec.

Kdyby někdy Čína: **TapTap Global** (mimo pevninskou Čínu, indie
friendly) je jediná realistická cesta; Bilibili/Douyin vyžadují čínský
telefon/ID nebo firmu. Do §9 jen pro případ, že by vznikla firma.

### LINE, KakaoTalk, WhatsApp (krátce, neověřeno)

| Platforma | Kde | Vlastní prostor | Komunity | Verdikt |
|---|---|---|---|---|
| LINE | JP, TW, TH | Official Account + Messaging API (push odběratelům, opt-in) | OpenChat bez bot API | až budou japonští hráči; OA prý jde založit i jako jednotlivec — ověřit |
| KakaoTalk | KR | Kakao Channel (ověřený vyžaduje korejskou firmu) | Open Chat bez API | ručně |
| WhatsApp | BR, Afrika, IN | Channels (broadcast; API pro postování neověřené), Business Cloud API jen opt-in konverzace | skupiny bez automatizace | ručně přes lidi v komunitě |

Společné: všechny jsou opt-in vysílání stávajícím hráčům, ne akvizice.
Pořadí: Telegram hned (infrastruktura botů už stojí), LINE až s japonskou
verzí postů na X, ostatní ne.

## 4. Regionální komunity — verdikty (**neověřeno**, viz §9)

| Region | Platforma | Čtení | Psaní | Verdikt |
|---|---|---|---|---|
| **Japonsko** | X/Twitter | placené | ✅ máme | Japonské varianty postů na X (`#インディーゲーム #シューティング`), přeložený tagline; Kirian je shmup = japonský žánr, tady je největší páka |
| | Mastodon (mstdn.jp, pawoo) | ✅ API | ✅ Postiz | pokrývá §3 |
| | note.com | bez oficiálního API | — | ruční: delší článek o vývoji, jednou za čas |
| | 5channel | ne | ne | skip |
| **Korea** | Naver Search API (blog, cafearticle, news) | ✅ API, registrace na developers.naver.com | — | `naver-monitor`: kdo o hře píše; jen čtení |
| | Naver Cafe / Blog, Ruliweb, DCInside | — | bez použitelného API | ruční; korejský text připraví agent |
| **Brazílie** | Reddit r/gamesEcultura, r/brasil, r/brdev | ✅ máme | ruční | jen přidat subreddity do `reddit-monitor` |
| | TabNews | ✅ REST API (open source) | login session | dev obsah, ne herní promo; nízká priorita |
| | Discord/WhatsApp skupiny | — | — | ruční |
| **Španělsky (LatAm, ES)** | r/argentina, r/mexico, r/es, r/videojuegos | ✅ máme | ruční | subreddity do monitoru |
| | Taringa!, Menéame, Forocoches | nejasné/bez API | — | skip / ruční |
| **Afrika** | X, WhatsApp/Telegram skupiny | — | — | ruční; hlavně mobilní appky, ne hry |
| | r/southafrica, r/Nigeria, r/Kenya | ✅ máme | ruční | subreddity do monitoru |
| | Nairaland, MyBroadband | bez API | — | ruční |
| | tech média (TechCabal, Techpoint) | — | — | pitch e-mailem, ne komunita |
| **UK** | r/indiegames, r/iosgaming, r/shmups (globál) | ✅ máme | ruční | už pokryto |
| | Pocket Gamer / TouchArcade fóra | bez API | — | ruční (TouchArcade Upcoming Games thread je pro iOS hry standard) |
| **DE/AT/CH** | r/de, r/de_EDV; GameStar/Computerbase fóra | ✅ / ne | ruční | subreddity do monitoru; fóra ručně |
| **FR** | r/france; jeuxvideo.com | ✅ / ne | ruční | totéž |
| **PL** | r/Polska; Wykop (API v3 existuje?) | ✅ / ? | ruční | ověřit Wykop API |
| **CZ/SK** | r/czech, r/Slovakia; Games.cz, Zing, herní Discordy | ✅ / ne | ruční | subreddity do monitoru; české posty CZ (pravidlo už je) |
| **NL / Nordics** | Tweakers.net, Flashback | bez API | — | ruční |

Společný jmenovatel: **Reddit už pokrýváme**, stačí do `reddit-monitor`
přidat regionální subreddity a do `PROMO_RULES.md` jazyk per subreddit.
Skutečně lokální platformy (Naver, note, fóra) nemají API pro psaní a
self-promo tam vyžaduje člověka, který jazyk a normy zná — agent tam umí
jen připravit text a přeložit ho.

## 5. Globální indie/dev platformy (mimo sociální sítě)

| Platforma | Co | Verdikt |
|---|---|---|
| **Luanti ContentDB** (DoggioWars) | recenze a vlákna u balíčku; ContentDB má JSON API (`/api/packages/…`) | `contentdb-monitor` pro recenze DoggioWars — přesná obdoba `store-reviews` (ověřit endpoint) |
| itch.io | devlogy, komunita; API jen pro nahrávání/prodeje | stránka + devlog ručně (agent napíše), butler pro build |
| IndieDB / ModDB | profil hry, novinky | ruční; DoggioWars sedí do ModDB |
| Steam Community | oznámení přes Steamworks | až bude Kirian na Steamu |
| Product Hunt | launch den | ruční, jednorázově per app |
| Hacker News | Show HN | ruční, jen pro dev článek (viz níže) |
| dev.to / Hashnode / Medium | Postiz providery (API key) | jen když vznikne dlouhý obsah |

**Dlouhý obsah** (dev blog): dnes systém dělá jen krátké posty. Kdyby měl
smysl dev blog (HN, note.com, dev.to, Hatena), je to nový pipeline:
`promo posts draft --kind article` (markdown, bez limitu), publikace přes
Postiz na dev.to/Hashnode/Medium, a odkaz z HN/Redditu ručně. Nedoporučuju
dřív, než poběží týden základní provoz.

## 6. Pořadí podle dosah/hodina práce

1. **Reddit: regionální subreddity + jazyk** (0 kódu, jen `PROMO_RULES.md`
   a seznam subredditů pro weekly-plan) — hned.
2. **Japonské posty na X** (0 kódu; pravidlo „u shmupu vždy JP varianta")
   — hned, po připojení X.
3. **Telegram kanál + diskusní skupina** + `telegram-monitor` (~0,5 dne;
   Bot API kód už v repu je).
4. **Mastodon** post + `mastodon-mentions` + hashtag monitor (~0,5 dne).
5. **Vlastní Discord** + `discord-monitor` (~1 den + tvoje založení serveru).
6. **Lemmy** post + `lemmy-monitor` (~0,5 dne, sdílí kód s Redditem).
7. **ContentDB monitor** pro DoggioWars (~0,5 dne, ověřit API).
8. **Naver Search monitor** (~0,5 dne, registrace na developers.naver.com).
9. LINE Official Account — až s japonskou lokalizací.

Vše ostatní (WeChat, KakaoTalk, WhatsApp, fóra bez API) = ruční
s podporou agenta (připraví a přeloží text), nebo skip.

## 7. Co se změní v kódu (společné pro 3–7)

- `posts.platform` CHECK rozšířit (`discord`, `mastodon`, `lemmy`,
  `telegram`) + `core/rules.go` limity (Mastodon 500 znaků, Lemmy titulek
  + tělo, Discord 2000) + `publisher.settings()` per platforma.
- Nový monitor = stejná šablona jako `internal/monitor/bluesky`: klient se
  stdlib, `--since`, `[]` místo `null`, secrets jen z env, httptest testy.
  Přidat řádek do `deploy/spark/promo-ingest`, SKILL.md, allowlist v
  `setup-openclaw.sh`.
- `PROMO_RULES.md`: tabulka platforma → jazyk, limit, co je povolené
  (oznámení vs. promo), seznam cizích Discord serverů/fór s pravidly.
- `projects.yaml`: per projekt `communities:` (subreddity, Lemmy komunity,
  Discord kanály), aby weekly-plan a reddit-scan věděly, kde hledat.

## 8. Co musí udělat Ol1n (nejde automatizovat)

- Telegram: veřejný kanál + diskusní skupina, dva další boti u BotFathera
  (`ol1n_postiz_bot` admin kanálu, `ol1n_community_bot` ve skupině
  s vypnutým privacy mode).
- Discord: server, aplikace + bot, intent, pozvat bota.
- Mastodon: založit účet na zvolené instanci (doporučení: `mastodon.social`
  nebo `mastodon.gamedev.place` pro indie dev — ověřit, že existuje).
- Lemmy: účet na `lemmy.world`.
- Naver: developer registrace (může vyžadovat korejský telefon — ověřit).
- Cizí komunity: přidat se jako člověk, přečíst pravidla, zapsat je do
  `PROMO_RULES.md`.

## 9. Ověřit před implementací (research se přerušil)

- Discord: aktuální hranice pro privilegované intenty; limity webhooků;
  `permissions=377957124096` = jaká práva (dekódovat bitmask).
- Naver Search API: kvóty, registrace, zda `cafearticle` vrací obsah nebo
  jen odkazy; existence Cafe write API a jeho podmínky.
- note.com: potvrdit, že oficiální API není.
- TabNews: `GET /api/v1/contents` tvar, pravidla pro promo.
- Wykop API v3, Menéame API, Taringa stav.
- Luanti ContentDB API pro recenze (`/api/packages/<author>/<name>/`?).
- Mastodon: `mastodon.gamedev.place` existence a pravidla; JP instance a
  jejich pravidla pro propagaci.
- Lemmy: pravidla `/c/indiegaming` a `/c/games` k self-promo.
- Threads API: stav app review pro malé vývojáře.
- Telegram: aktuální API ToS k userbotům (čtení veřejných skupin pod
  vlastním účtem); podmínky Telegram Ads; limity kanálu (`t.me/c/…` URL
  formát pro supergroup s navázaným kanálem).
- WeChat: aktuální podmínky registrace Official Account pro zahraniční
  firmu (jen kdyby vznikla s.r.o.); TapTap Global podmínky pro indie.
- LINE Official Account: může ho založit jednotlivec mimo Japonsko?
  Messaging API limity free plánu.
- WhatsApp Channels: existuje API pro publikaci do kanálu?
