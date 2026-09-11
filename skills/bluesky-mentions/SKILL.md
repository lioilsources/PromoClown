---
name: bluesky-mentions
description: Read-only fetch of Bluesky mentions, replies and quotes of Ol1n's account with the `bluesky-mentions` CLI, for importing into promo mentions.
metadata: {"openclaw": {"emoji": "🦋", "os": ["linux"], "requires": {"bins": ["bluesky-mentions", "promo"]}}}
---

# bluesky-mentions

```bash
bluesky-mentions fetch --since 48h | promo mentions import
```

- Prints a JSON array of notifications with reason mention, reply or quote.
  `url` opens the post on bsky.app.
- Does not mark notifications as read and never posts.
- The login session is cached; Bluesky rate-limits fresh logins, so do not
  delete the session file or loop this command.

Usually run by `promo-ingest` during the heartbeat.
