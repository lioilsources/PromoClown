-- Mentions ------------------------------------------------------------------

-- Conflict returns no row: the caller treats sql.ErrNoRows as "already known".
-- name: InsertMention :one
INSERT INTO mentions (
    platform, kind, external_id, url, author, text, context, project_id, posted_at, seen_at
) VALUES (
    sqlc.arg('platform'), sqlc.arg('kind'), sqlc.arg('external_id'), sqlc.arg('url'),
    sqlc.arg('author'), sqlc.arg('text'), sqlc.arg('context'), sqlc.narg('project_id'),
    sqlc.narg('posted_at'), sqlc.arg('now')
)
ON CONFLICT (platform, external_id) DO NOTHING
RETURNING id;

-- name: ListMentions :many
SELECT * FROM mentions
WHERE (CAST(sqlc.narg('handled') AS BOOLEAN) IS NULL OR handled = CAST(sqlc.narg('handled') AS BOOLEAN))
  AND (CAST(sqlc.narg('platform') AS TEXT) IS NULL OR platform = CAST(sqlc.narg('platform') AS TEXT))
ORDER BY COALESCE(posted_at, seen_at) DESC
LIMIT sqlc.arg('limit');

-- Returns and marks in one statement, so two heartbeats never report the same item.
-- name: ClaimNewMentions :many
UPDATE mentions SET notified_at = sqlc.arg('now')
WHERE notified_at IS NULL AND handled = 0
RETURNING *;

-- name: SetMentionHandled :execrows
UPDATE mentions SET handled = 1, notified_at = COALESCE(notified_at, sqlc.arg('now'))
WHERE id = sqlc.arg('id');

-- name: MentionsSeenSince :many
SELECT * FROM mentions WHERE seen_at >= sqlc.arg('since') ORDER BY seen_at;

-- Reddit's data policy asks for stored user content to be dropped within 48 h.
-- The row stays (external_id keeps the import idempotent, url keeps the link).
-- name: RedactMentions :execrows
UPDATE mentions SET text = '', author = ''
WHERE platform = sqlc.arg('platform') AND seen_at < sqlc.arg('before')
  AND (text != '' OR author != '');

-- Reviews -------------------------------------------------------------------

-- name: InsertReview :one
INSERT INTO reviews (
    store, app_id, project_id, external_id, rating, title, text, author, version,
    territory, posted_at, seen_at
) VALUES (
    sqlc.arg('store'), sqlc.arg('app_id'), sqlc.narg('project_id'), sqlc.arg('external_id'),
    sqlc.arg('rating'), sqlc.arg('title'), sqlc.arg('text'), sqlc.arg('author'),
    sqlc.arg('version'), sqlc.arg('territory'), sqlc.narg('posted_at'), sqlc.arg('now')
)
ON CONFLICT (store, external_id) DO NOTHING
RETURNING id;

-- name: ListReviews :many
SELECT * FROM reviews
WHERE (CAST(sqlc.narg('handled') AS BOOLEAN) IS NULL OR handled = CAST(sqlc.narg('handled') AS BOOLEAN))
ORDER BY COALESCE(posted_at, seen_at) DESC
LIMIT sqlc.arg('limit');

-- name: ClaimNewReviews :many
UPDATE reviews SET notified_at = sqlc.arg('now')
WHERE notified_at IS NULL AND handled = 0
RETURNING *;

-- name: SetReviewHandled :execrows
UPDATE reviews SET handled = 1, notified_at = COALESCE(notified_at, sqlc.arg('now'))
WHERE id = sqlc.arg('id');

-- name: ReviewsSeenSince :many
SELECT * FROM reviews WHERE seen_at >= sqlc.arg('since') ORDER BY seen_at;
