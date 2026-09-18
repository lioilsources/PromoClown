-- name: EnqueueTribute :one
INSERT INTO tributes (project_id, credit, source_path, note, submitted_by, created_at, updated_at)
VALUES (sqlc.arg('project_id'), sqlc.arg('credit'), sqlc.arg('source_path'), sqlc.arg('note'),
        sqlc.arg('submitted_by'), sqlc.arg('now'), sqlc.arg('now'))
RETURNING *;

-- name: GetTribute :one
SELECT * FROM tributes WHERE id = ?;

-- name: ListTributes :many
SELECT * FROM tributes
WHERE CAST(sqlc.narg('status') AS TEXT) IS NULL OR status = CAST(sqlc.narg('status') AS TEXT)
ORDER BY id DESC
LIMIT sqlc.arg('limit');

-- Claim the oldest queued tribute for this run. The status change is the lock:
-- a second worker started by accident finds nothing left to take.
-- name: ClaimTribute :one
UPDATE tributes
SET status = 'running', updated_at = sqlc.arg('now')
WHERE id = (SELECT id FROM tributes WHERE status = 'queued' ORDER BY id LIMIT 1)
RETURNING *;

-- name: TributeDrafted :one
UPDATE tributes
SET status = 'drafted',
    post_id = sqlc.arg('post_id'),
    styles = sqlc.arg('styles'),
    error = '',
    updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status = 'running'
RETURNING *;

-- name: TributeFailed :one
UPDATE tributes
SET status = 'failed', error = sqlc.arg('error'), updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status IN ('queued', 'running')
RETURNING *;

-- A failed tribute goes back in the queue; cancelling drops it for good.
-- name: RequeueTribute :one
UPDATE tributes
SET status = 'queued', error = '', updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status IN ('failed', 'cancelled')
RETURNING *;

-- name: CancelTribute :one
UPDATE tributes
SET status = 'cancelled', updated_at = sqlc.arg('now')
WHERE id = sqlc.arg('id') AND status IN ('queued', 'failed')
RETURNING *;

-- name: CountQueuedTributes :one
SELECT COUNT(*) FROM tributes WHERE status = 'queued';
