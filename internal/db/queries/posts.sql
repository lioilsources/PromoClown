-- name: CreatePost :one
INSERT INTO posts (
    project_id, platform, account, kind, title, text, media_paths, reply_to_url, warnings,
    created_by, status, created_at, updated_at
) VALUES (
    sqlc.arg('project_id'), sqlc.arg('platform'), sqlc.arg('account'), sqlc.arg('kind'),
    sqlc.arg('title'), sqlc.arg('text'), sqlc.arg('media_paths'), sqlc.arg('reply_to_url'),
    sqlc.arg('warnings'), sqlc.arg('created_by'), 'draft', sqlc.arg('now'), sqlc.arg('now')
)
RETURNING *;

-- name: GetPost :one
SELECT * FROM posts WHERE id = ?;

-- name: ListPosts :many
SELECT * FROM posts
WHERE (CAST(sqlc.narg('status') AS TEXT) IS NULL OR status = CAST(sqlc.narg('status') AS TEXT))
  AND (CAST(sqlc.narg('project_id') AS INTEGER) IS NULL OR project_id = CAST(sqlc.narg('project_id') AS INTEGER))
  AND (CAST(sqlc.narg('platform') AS TEXT) IS NULL OR platform = CAST(sqlc.narg('platform') AS TEXT))
ORDER BY id DESC
LIMIT sqlc.arg('limit');

-- What went out (or is committed to go out) and when. The agent reads this
-- before every proposal.
-- name: PostLog :many
SELECT * FROM posts
WHERE status IN ('approved', 'scheduled', 'published')
  AND COALESCE(published_at, scheduled_at, approved_at) >= sqlc.arg('since')
ORDER BY COALESCE(published_at, scheduled_at, approved_at) DESC;

-- Every post created in the window, whatever its fate: re-proposing a text
-- that was rejected is as pointless as repeating a published one.
-- name: PostsCreatedSince :many
SELECT * FROM posts
WHERE created_at >= sqlc.arg('since')
ORDER BY id;

-- Only the same account: two projects posting from two X accounts never
-- compete for the same day.
-- name: CommittedPostsBetween :many
SELECT * FROM posts
WHERE status IN ('approved', 'scheduled', 'published')
  AND platform = sqlc.arg('platform')
  AND account = sqlc.arg('account')
  AND COALESCE(published_at, scheduled_at) >= sqlc.arg('from')
  AND COALESCE(published_at, scheduled_at) < sqlc.arg('to')
ORDER BY COALESCE(published_at, scheduled_at);

-- name: UpdateDraftContent :one
UPDATE posts
SET text = sqlc.arg('text'),
    title = sqlc.arg('title'),
    media_paths = sqlc.arg('media_paths'),
    warnings = sqlc.arg('warnings'),
    revision = revision + 1,
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status = 'draft'
RETURNING *;

-- name: ApprovePost :one
UPDATE posts
SET status = 'approved',
    approved_at = sqlc.arg('now'),
    approved_by = sqlc.arg('actor'),
    scheduled_at = sqlc.arg('scheduled_at'),
    error = '',
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status = 'draft' AND revision = sqlc.arg('revision')
RETURNING *;

-- name: RejectPost :one
UPDATE posts
SET status = 'rejected',
    error = sqlc.arg('reason'),
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status IN ('draft', 'approved', 'failed')
RETURNING *;

-- A failed post goes back to approved so the publisher picks it up again.
-- name: RetryPost :one
UPDATE posts
SET status = 'approved',
    error = '',
    scheduled_at = sqlc.arg('scheduled_at'),
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status = 'failed'
RETURNING *;

-- name: ListApprovedForPostiz :many
SELECT * FROM posts
WHERE status = 'approved' AND platform != 'reddit'
ORDER BY scheduled_at, id;

-- name: ListScheduled :many
SELECT * FROM posts WHERE status = 'scheduled' ORDER BY scheduled_at, id;

-- name: MarkScheduled :one
UPDATE posts
SET status = 'scheduled',
    postiz_post_id = sqlc.arg('postiz_post_id'),
    postiz_group = sqlc.arg('postiz_group'),
    scheduled_at = sqlc.arg('scheduled_at'),
    error = '',
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status = 'approved'
RETURNING *;

-- name: MarkPublished :one
UPDATE posts
SET status = 'published',
    published_at = sqlc.arg('published_at'),
    release_url = sqlc.arg('release_url'),
    error = '',
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status IN ('approved', 'scheduled')
RETURNING *;

-- name: MarkFailed :one
UPDATE posts
SET status = 'failed',
    error = sqlc.arg('error'),
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status IN ('approved', 'scheduled')
RETURNING *;

-- Transient publisher error: keep the state, remember why it did not move.
-- name: SetPostError :exec
UPDATE posts SET error = sqlc.arg('error'), updated_at = sqlc.arg('now') WHERE id = sqlc.arg('id');

-- name: FindPostByPostizRef :one
SELECT * FROM posts
WHERE (postiz_post_id != '' AND postiz_post_id = sqlc.arg('ref'))
   OR (postiz_group != '' AND postiz_group = sqlc.arg('ref'))
LIMIT 1;

-- name: FindPublishedByReleaseURL :one
SELECT * FROM posts
WHERE platform = sqlc.arg('platform') AND release_url != ''
  AND instr(release_url, sqlc.arg('needle')) > 0
ORDER BY id DESC
LIMIT 1;

-- name: ListDraftsToNotify :many
SELECT * FROM posts
WHERE status = 'draft' AND notified_revision != revision
ORDER BY id;

-- name: SetPostNotified :exec
UPDATE posts
SET telegram_msg_id = sqlc.arg('telegram_msg_id'),
    notified_revision = sqlc.arg('revision')
WHERE id = sqlc.arg('id');

-- name: AddPostEvent :exec
INSERT INTO post_events (post_id, action, actor, detail, notified, created_at)
VALUES (sqlc.arg('post_id'), sqlc.arg('action'), sqlc.arg('actor'), sqlc.arg('detail'),
        sqlc.arg('notified'), sqlc.arg('now'));

-- name: ListPostEvents :many
SELECT * FROM post_events WHERE post_id = ? ORDER BY id;

-- name: ListUnnotifiedEvents :many
SELECT * FROM post_events WHERE notified = 0 ORDER BY id LIMIT 50;

-- name: MarkEventNotified :exec
UPDATE post_events SET notified = 1 WHERE id = ?;
