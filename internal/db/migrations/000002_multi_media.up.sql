-- One post can carry several images: X and Bluesky both take up to four, and
-- the tribute posts (one source picture restyled four ways) need all four.
-- The column holds one asset path per line, in posting order.
ALTER TABLE posts RENAME COLUMN media_path TO media_paths;
