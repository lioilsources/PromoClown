-- name: ListProjects :many
SELECT * FROM projects
WHERE CAST(sqlc.narg('status') AS TEXT) IS NULL OR status = CAST(sqlc.narg('status') AS TEXT)
ORDER BY name;

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE slug = ?;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = ?;

-- name: FindProjectByAppID :one
SELECT * FROM projects
WHERE (ios_app_id != '' AND ios_app_id = sqlc.arg('app_id'))
   OR (android_package != '' AND android_package = sqlc.arg('app_id'))
LIMIT 1;

-- name: UpsertProject :one
INSERT INTO projects (
    slug, name, tagline, audience, tags, hooks, forbidden_claims, website_url,
    store_ios_url, store_android_url, ios_app_id, android_package, assets_dir,
    status, created_at, updated_at
) VALUES (
    sqlc.arg('slug'), sqlc.arg('name'), sqlc.arg('tagline'), sqlc.arg('audience'),
    sqlc.arg('tags'), sqlc.arg('hooks'), sqlc.arg('forbidden_claims'), sqlc.arg('website_url'),
    sqlc.arg('store_ios_url'), sqlc.arg('store_android_url'), sqlc.arg('ios_app_id'),
    sqlc.arg('android_package'), sqlc.arg('assets_dir'), sqlc.arg('status'),
    sqlc.arg('now'), sqlc.arg('now')
)
ON CONFLICT (slug) DO UPDATE SET
    name              = excluded.name,
    tagline           = excluded.tagline,
    audience          = excluded.audience,
    tags              = excluded.tags,
    hooks             = excluded.hooks,
    forbidden_claims  = excluded.forbidden_claims,
    website_url       = excluded.website_url,
    store_ios_url     = excluded.store_ios_url,
    store_android_url = excluded.store_android_url,
    ios_app_id        = excluded.ios_app_id,
    android_package   = excluded.android_package,
    assets_dir        = excluded.assets_dir,
    status            = excluded.status,
    updated_at        = excluded.updated_at
RETURNING *;
