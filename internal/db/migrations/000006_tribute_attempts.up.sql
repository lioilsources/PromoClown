-- How many runs have failed on a tribute. promo-tribute puts failed rows back
-- in the queue at the start of each run until this reaches its retry limit, so
-- a tribute cut off by the run's time limit is not lost.
ALTER TABLE tributes ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
