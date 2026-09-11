---
name: youtube-comments
description: Read-only fetch of new comments on Ol1n's YouTube channel with the `youtube-comments` CLI, for importing into promo mentions.
metadata: {"openclaw": {"emoji": "▶️", "os": ["linux"], "requires": {"bins": ["youtube-comments", "promo"]}}}
---

# youtube-comments

```bash
youtube-comments fetch --since 48h | promo mentions import
```

- Prints a JSON array of top-level comments on all channel videos, newest first,
  skipping the channel's own comments. `context` is the video id, `url` links
  straight to the comment.
- promo maps a comment to the project whose published YouTube post has that video.
- Costs 1 YouTube API quota unit per page (100 comments); `--max-pages` caps it (default 3).
- Read-only. Never reply on YouTube; tell Ol1n which comments deserve an answer.

Usually run by `promo-ingest` during the heartbeat.
