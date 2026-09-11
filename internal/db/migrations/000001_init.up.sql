-- Timestamps are TEXT in RFC 3339 UTC without fractional seconds
-- ("2026-09-14T10:00:00Z"), so plain string comparison orders them correctly.

CREATE TABLE projects (
    id                INTEGER PRIMARY KEY,
    slug              TEXT    NOT NULL UNIQUE,
    name              TEXT    NOT NULL,
    tagline           TEXT    NOT NULL DEFAULT '',
    audience          TEXT    NOT NULL DEFAULT '',
    tags              TEXT    NOT NULL DEFAULT '',  -- comma separated
    hooks             TEXT    NOT NULL DEFAULT '',  -- one hook per line
    forbidden_claims  TEXT    NOT NULL DEFAULT '',  -- one claim per line
    website_url       TEXT    NOT NULL DEFAULT '',
    store_ios_url     TEXT    NOT NULL DEFAULT '',
    store_android_url TEXT    NOT NULL DEFAULT '',
    ios_app_id        TEXT    NOT NULL DEFAULT '',  -- App Store Connect app id, maps reviews
    android_package   TEXT    NOT NULL DEFAULT '',  -- Play package name, maps reviews
    assets_dir        TEXT    NOT NULL DEFAULT '',  -- relative to PROMO_ASSETS_DIR, default slug
    status            TEXT    NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'paused', 'archived')),
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE TABLE posts (
    id                INTEGER PRIMARY KEY,
    project_id        INTEGER NOT NULL REFERENCES projects (id),
    platform          TEXT    NOT NULL
                      CHECK (platform IN ('bluesky', 'x', 'youtube', 'reddit')),
    kind              TEXT    NOT NULL DEFAULT 'post'
                      CHECK (kind IN ('post', 'short', 'video', 'reply')),
    title             TEXT    NOT NULL DEFAULT '',  -- YouTube title
    text              TEXT    NOT NULL,
    media_path        TEXT    NOT NULL DEFAULT '',  -- relative to PROMO_ASSETS_DIR
    reply_to_url      TEXT    NOT NULL DEFAULT '',  -- Reddit thread/comment being answered
    warnings          TEXT    NOT NULL DEFAULT '',  -- rule warnings shown to the approver, one per line
    status            TEXT    NOT NULL DEFAULT 'draft'
                      CHECK (status IN ('draft', 'approved', 'scheduled', 'published', 'rejected', 'failed')),
    -- Bumped on every content change. Approval names the revision it saw, so an
    -- edit between "preview sent" and "ok" can never approve unseen text.
    revision          INTEGER NOT NULL DEFAULT 1,
    created_by        TEXT    NOT NULL DEFAULT 'agent',
    postiz_post_id    TEXT    NOT NULL DEFAULT '',
    postiz_group      TEXT    NOT NULL DEFAULT '',
    release_url       TEXT    NOT NULL DEFAULT '',
    error             TEXT    NOT NULL DEFAULT '',
    scheduled_at      TEXT,
    published_at      TEXT,
    approved_at       TEXT,
    approved_by       TEXT    NOT NULL DEFAULT '',
    telegram_msg_id   INTEGER,
    notified_revision INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX posts_status ON posts (status);
CREATE INDEX posts_platform_scheduled ON posts (platform, scheduled_at);
CREATE INDEX posts_created ON posts (created_at);

-- Audit trail of every state change, and the outbox for Telegram status
-- notifications (notified = 0 means the approval bot has not reported it yet).
CREATE TABLE post_events (
    id         INTEGER PRIMARY KEY,
    post_id    INTEGER NOT NULL REFERENCES posts (id),
    action     TEXT    NOT NULL,
    actor      TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    notified   BOOLEAN NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL
);

CREATE INDEX post_events_post ON post_events (post_id);
CREATE INDEX post_events_unnotified ON post_events (notified) WHERE notified = 0;

CREATE TABLE mentions (
    id          INTEGER PRIMARY KEY,
    platform    TEXT    NOT NULL,
    kind        TEXT    NOT NULL DEFAULT 'mention',  -- mention|reply|quote|comment|message|search
    external_id TEXT    NOT NULL,
    url         TEXT    NOT NULL DEFAULT '',
    author      TEXT    NOT NULL DEFAULT '',
    text        TEXT    NOT NULL DEFAULT '',
    context     TEXT    NOT NULL DEFAULT '',  -- video id, subreddit, parent post uri
    project_id  INTEGER REFERENCES projects (id),
    posted_at   TEXT,
    seen_at     TEXT    NOT NULL,
    notified_at TEXT,
    handled     BOOLEAN NOT NULL DEFAULT 0,
    UNIQUE (platform, external_id)
);

CREATE INDEX mentions_pending ON mentions (handled, notified_at);

CREATE TABLE reviews (
    id          INTEGER PRIMARY KEY,
    store       TEXT    NOT NULL CHECK (store IN ('appstore', 'googleplay')),
    app_id      TEXT    NOT NULL,
    project_id  INTEGER REFERENCES projects (id),
    external_id TEXT    NOT NULL,
    rating      INTEGER NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    text        TEXT    NOT NULL DEFAULT '',
    author      TEXT    NOT NULL DEFAULT '',
    version     TEXT    NOT NULL DEFAULT '',
    territory   TEXT    NOT NULL DEFAULT '',
    posted_at   TEXT,
    seen_at     TEXT    NOT NULL,
    notified_at TEXT,
    handled     BOOLEAN NOT NULL DEFAULT 0,
    UNIQUE (store, external_id)
);

CREATE INDEX reviews_pending ON reviews (handled, notified_at);
