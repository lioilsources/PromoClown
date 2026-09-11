Weekly promo plan. Follow AGENTS.md and PROMO_RULES.md.

1. Read PROMO_RULES.md and PROJECTS.md. Only projects with status active may be promoted.
2. Run `promo posts log --days 30` and `promo posts list --status draft`.
3. For each active project decide whether there is something new, specific and honest to say this week. Skip projects where there is not.
4. For the projects you keep, run `promo projects assets <slug>` and choose one visual per draft (a vertical mp4 under shorts/ for YouTube).
5. Optional Reddit check: `reddit-monitor search --sub <2-5 subreddits that fit the project> --keywords "<2-4 short keywords>" --since 7d`. Keep at most two threads where someone asks for exactly what an app does, save each with `promo mentions add`, and draft at most one reply per subreddit.
6. Create 3 to 5 drafts in total with `promo posts draft`, spread over projects and platforms (bluesky, x, youtube). If a draft is refused (exit status 2), fix every listed problem and retry once.
7. Reply to Ol1n in Czech: one line per draft `#id · platform · project — first words`, then one sentence on why these. End with: schvaluj v approval botovi.

If nothing is worth posting this week, say that in one sentence instead.
