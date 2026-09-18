-- Posts drafted with more than one asset keep all the paths in the single
-- column; the publisher of the old schema would then upload nothing, because
-- the multi-line value is not a readable filename. Such posts must be edited
-- back to one asset before this migration runs.
ALTER TABLE posts RENAME COLUMN media_paths TO media_path;
