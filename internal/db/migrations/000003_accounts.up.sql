-- Until now one platform meant one account, so the frequency rules could count
-- posts per platform. Kirian and Tsumiki each post to their own X account, and
-- a busy day on one must not block the other — so a post records the account it
-- was drafted for, and the rules count per account.
--
-- postiz_accounts maps a platform to the Postiz channel that publishes it, one
-- "platform=channel" per line (channel is the integration id or its name in
-- Postiz). A platform missing from the map falls back to POSTIZ_INTEGRATIONS.
ALTER TABLE projects ADD COLUMN postiz_accounts TEXT NOT NULL DEFAULT '';

-- Caps that used to be hard-coded: at most one post per account per day, and at
-- most one post per project per platform in seven days. The defaults keep that
-- behaviour; the tribute posts raise the daily cap and drop the gap to zero.
ALTER TABLE projects ADD COLUMN daily_cap INTEGER NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN min_days_between INTEGER NOT NULL DEFAULT 7;

-- Empty means the platform's default channel. Recorded at draft time, so a
-- later remapping does not rewrite what history says went where.
ALTER TABLE posts ADD COLUMN account TEXT NOT NULL DEFAULT '';

CREATE INDEX posts_account_scheduled ON posts (platform, account, scheduled_at);
