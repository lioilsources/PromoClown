Weekly promo plan. Follow AGENTS.md and PROMO_RULES.md.

1. Read PROMO_RULES.md and PROJECTS.md. Only projects with status active may be promoted.
2. Run `promo posts log --days 30` and `promo posts list --status draft`.
3. For each active project decide whether there is something new, specific and honest to say this week. Skip projects where there is not.
4. For the projects you keep, run `promo projects assets <slug>` and choose one visual per draft (a vertical mp4 under shorts/ for YouTube).
5. Reddit search links. Reddit refused API access for this, so never run `reddit-monitor` and never open reddit.com yourself: Ol1n searches by hand. For each project you keep, pick 2-5 subreddits that fit it and whose rules allow self-promotion, and 2-4 short keywords, then build one link per subreddit: `https://www.reddit.com/r/<subreddit>/search/?q=<query>&restrict_sr=1&sort=new&t=week`. The query is the keywords joined with OR, phrases in quotes, URL-encoded: `"offline maps" OR "trail app"` becomes `q=%22offline+maps%22+OR+%22trail+app%22`.
6. Create 3 to 5 drafts in total with `promo posts draft`, spread over projects and platforms (bluesky, x, youtube). If a draft is refused (exit status 2), fix every listed problem and retry once.
7. Reply to Ol1n in Czech: one line per draft `#id · platform · project — first words`, then one sentence on why these. End with: schvaluj v approval botovi. Then the Reddit links under the heading `Reddit k prokliknutí:`, one line each `project · r/subreddit — link`, ending with: pošli mi odkaz na vlákno, kde se někdo ptá přesně na to, co aplikace dělá, a navrhnu odpověď.

If nothing is worth posting this week, say that in one sentence instead, with no Reddit links.
