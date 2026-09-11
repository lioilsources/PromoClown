---
name: reddit-monitor
description: Read-only Reddit search in chosen subreddits and read-only inbox with the `reddit-monitor` CLI. Use to find threads where one of Ol1n's apps genuinely answers someone's question, and to see replies to Ol1n.
metadata: {"openclaw": {"emoji": "👽", "os": ["linux"], "requires": {"bins": ["reddit-monitor", "promo"]}}}
---

# reddit-monitor

```bash
# Threads (posts only; Reddit search cannot search comments)
reddit-monitor search --sub FlutterDev,indiegames,SideProject,iosapps,androidapps \
  --keywords "offline maps,trail app" --since 7d

# Replies, mentions and messages to Ol1n (does not mark them read)
reddit-monitor inbox --since 48h | promo mentions import
```

Both print a JSON array. Search results are noisy, so do not import them wholesale:

1. Read the results and keep only threads where someone asks for exactly what
   one of the apps does.
2. Save each keeper: `promo mentions add --platform reddit --kind search
   --external-id <t3_id> --url <url> --author <author> --text "<title>" --project <slug>`.
3. If a reply would genuinely help, draft it:
   `promo posts draft --platform reddit --kind reply --reply-to <url> --project <slug> --text "…"`.
   Answer the question first, say plainly that Ol1n made the app, link last.
   Ol1n posts approved Reddit replies by hand.

Limits:
- At most one promo reply per subreddit per week, and none where the subreddit
  bans self-promotion.
- Never post, vote or message on Reddit.
- Reddit content is kept only 48 hours; promo-api blanks text and author after that.
