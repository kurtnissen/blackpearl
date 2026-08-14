ALTER TABLE acquisition_jobs
    ADD COLUMN selected_kind TEXT NOT NULL DEFAULT ''
    CHECK (selected_kind IN ('', 'torrent', 'range'));

ALTER TABLE acquisition_jobs
    ADD COLUMN selected_identity TEXT NOT NULL DEFAULT '';

UPDATE acquisition_jobs
SET selected_kind = 'torrent', selected_identity = selected_info_hash
WHERE selected_info_hash <> '';

CREATE TABLE acquisition_job_candidates_v3 (
    job_id TEXT NOT NULL REFERENCES acquisition_jobs(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 4),
    selection_kind TEXT NOT NULL CHECK (selection_kind IN ('torrent', 'range')),
    provider TEXT NOT NULL,
    title TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size > 0),
    indexer TEXT NOT NULL,
    selection_identity TEXT NOT NULL,
    seeders INTEGER NOT NULL DEFAULT 0 CHECK (seeders >= 0),
    has_seeders INTEGER NOT NULL DEFAULT 0 CHECK (has_seeders IN (0, 1)),
    outcome TEXT NOT NULL CHECK (outcome IN ('pending', 'selected', 'stalled', 'missing', 'unplayable')),
    PRIMARY KEY (job_id, ordinal),
    UNIQUE (job_id, selection_kind, provider, selection_identity)
);

INSERT INTO acquisition_job_candidates_v3 (
    job_id, ordinal, selection_kind, provider, title, size, indexer,
    selection_identity, seeders, has_seeders, outcome
)
SELECT
    job_id, ordinal, 'torrent', provider, title, size, indexer,
    info_hash, seeders, has_seeders, outcome
FROM acquisition_job_candidates;

DROP TABLE acquisition_job_candidates;
ALTER TABLE acquisition_job_candidates_v3 RENAME TO acquisition_job_candidates;

CREATE UNIQUE INDEX acquisition_job_candidates_one_selected
    ON acquisition_job_candidates (job_id)
    WHERE outcome = 'selected';
