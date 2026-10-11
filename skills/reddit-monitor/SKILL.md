---
name: reddit-monitor
description: Reddit threads Ol1n found by hand. Use when Ol1n sends a Reddit link (and usually what the thread asks) to save it and draft a reply for him to post. Reddit is never searched or read from here.
metadata: {"openclaw": {"emoji": "👽", "os": ["linux"], "requires": {"bins": ["promo"]}}}
---

# reddit-monitor

Reddit refused API access for this use, so nothing here talks to Reddit: never
run the `reddit-monitor` CLI and never open reddit.com. Ol1n searches by hand
(the weekly plan gives him ready search links) and Reddit's own notifications
tell him about replies.

When Ol1n sends a thread link:

1. Take the thread id from the link: `…/r/<subreddit>/comments/<id>/…` gives
   `t3_<id>`. Use what Ol1n wrote about the thread as its text; if he sent only
   the link, ask him what the thread asks instead of guessing.
2. Save it: `promo mentions add --platform reddit --kind search
   --external-id t3_<id> --url <url> --text "<what the thread asks>" --project <slug>`.
3. If a reply would genuinely help, draft it:
   `promo posts draft --platform reddit --kind reply --reply-to <url> --project <slug> --text "…"`.
   Answer the question first, say plainly that Ol1n made the app, link last.
   Ol1n posts approved Reddit replies by hand.

Limits:
- At most one promo reply per subreddit per week, and none where the subreddit
  bans self-promotion.
- Never post, vote or message on Reddit.
- Reddit content is kept only 48 hours; promo-api blanks text and author after that.
