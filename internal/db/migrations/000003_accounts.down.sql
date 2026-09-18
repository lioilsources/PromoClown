DROP INDEX posts_account_scheduled;
ALTER TABLE posts DROP COLUMN account;
ALTER TABLE projects DROP COLUMN min_days_between;
ALTER TABLE projects DROP COLUMN daily_cap;
ALTER TABLE projects DROP COLUMN postiz_accounts;
