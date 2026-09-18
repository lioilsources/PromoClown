-- A tribute is one picture somebody else made, restyled four ways and posted
-- with a credit to its author ("Tribute to @handle ..."). The owner sends the
-- picture and the handle to the approval bot during the day; the night job
-- turns each queued row into a four-image draft, which still needs the usual
-- approval before anything is published.
CREATE TABLE tributes (
    id           INTEGER PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects (id),
    credit       TEXT    NOT NULL,                -- "@handle" the post thanks
    source_path  TEXT    NOT NULL,                -- picture as sent, relative to PROMO_ASSETS_DIR
    note         TEXT    NOT NULL DEFAULT '',     -- rest of the caption, for the approver
    status       TEXT    NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'running', 'drafted', 'failed', 'cancelled')),
    post_id      INTEGER REFERENCES posts (id),   -- draft made from it
    styles       TEXT    NOT NULL DEFAULT '',     -- style ids of the generated set, one per line
    error        TEXT    NOT NULL DEFAULT '',
    submitted_by TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

CREATE INDEX tributes_status ON tributes (status, id);
