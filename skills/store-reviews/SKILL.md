---
name: store-reviews
description: Read-only fetch of App Store and Google Play reviews for Ol1n's apps with the `store-reviews` CLI, for importing into promo. Use when checking what users say in the stores.
metadata: {"openclaw": {"emoji": "⭐", "os": ["linux"], "requires": {"bins": ["store-reviews", "promo"]}}}
---

# store-reviews

```bash
store-reviews fetch --since 48h | promo reviews import
store-reviews fetch --since 7d --store appstore      # one store only
```

- Prints a JSON array of reviews; `promo reviews import` stores the new ones and
  ignores those already known, so overlapping `--since` windows are harmless.
- A store without credentials is skipped with a note on stderr.
- Google Play only returns reviews created or edited in the last 7 days, and
  App Store reviews carry no app version.
- Read-only: the keys can read reviews, not answer them. Replies to reviews are
  Ol1n's job in App Store Connect / Play Console — you may suggest wording in
  your message to Ol1n.

Usually you do not call this directly: `promo-ingest` runs it during the heartbeat.
