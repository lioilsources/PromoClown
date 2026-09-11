# PROMO_RULES.md

Rules for every draft. promo-api enforces the measurable ones and refuses a
draft that breaks them; the rest are on you.

## Voice

- Human and concrete: what the app does, for whom, and one specific detail. Write like the developer who built it, in first person.
- No marketing phrases: *game-changer, revolutionary, unlock, elevate, seamless, must-have, next level, empower, supercharge, "we're excited to announce"*.
- No emoji spam: at most one or two emoji per post, none in YouTube titles.
- At most two hashtags on X and Bluesky, none on Reddit.
- English by default. Czech only for Czech communities (for example r/czech, r/Czechia).
- Never invent numbers, downloads, ratings, reviews, quotes or features. Never name or compare with competitors.
- Never make a claim listed under "Never claim or promise" for the project.

## Every post

- Link to the store (App Store or Google Play) or the project website. Prefer the store link.
- Attach exactly one visual: a screenshot or icon for Bluesky and X, a vertical mp4 for YouTube Shorts.
- One idea per post. Good angles: a feature someone asked for, a before/after, a behind-the-scenes detail from building it in Go or Flutter, a short gameplay moment, what changed in the last release.

## Frequency

- At most one post per platform per day.
- At most one post per project per week on the same platform.
- promo-api picks the publication slot that satisfies both when Ol1n approves; do not put dates or "today" in the text.
- Never repeat a text from the last 30 days (`promo posts log --days 30`). A near-identical text on the same platform is refused.

## Platforms

| Platform | Limits (enforced) | Notes |
|---|---|---|
| X | 280 weighted characters; every link counts as 23 | Hook in the first line. |
| Bluesky | 300 characters, links count in full | Slightly more personal than X. |
| YouTube Shorts | `--title` up to 100 characters; description up to 5000 bytes; mp4 required; no `<` or `>` | Title says what you see; description carries the store link. |
| Reddit | replies only, `--reply-to` required | See below. |

## Reddit

- Reply only where someone asks a question the app genuinely answers. No cold promotion posts.
- Answer the question first. Say plainly that Ol1n made the app ("I built X for this…"). Link last, and only if the subreddit allows links.
- At most one promo reply per subreddit per week, and none in subreddits whose rules ban self-promotion.
- Ol1n posts approved replies by hand.

## Weekly plan

- 3 to 5 drafts in total, spread over different projects and platforms.
- Mix: at most one draft per project per platform, at least one visual-led post (screenshot or short).
- If there is nothing new or honest to say about a project, skip it. Fewer good drafts beat five weak ones.
