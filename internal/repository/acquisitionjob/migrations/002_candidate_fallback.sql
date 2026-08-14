ALTER TABLE acquisition_jobs
    ADD COLUMN selected_candidate_ordinal INTEGER NOT NULL DEFAULT -1
    CHECK (selected_candidate_ordinal BETWEEN -1 AND 4);

ALTER TABLE acquisition_jobs
    ADD COLUMN created_by_job INTEGER NOT NULL DEFAULT 0
    CHECK (created_by_job IN (0, 1));

CREATE TABLE acquisition_job_candidates (
    job_id TEXT NOT NULL REFERENCES acquisition_jobs(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 4),
    provider TEXT NOT NULL,
    title TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size > 0),
    indexer TEXT NOT NULL,
    info_hash TEXT NOT NULL,
    seeders INTEGER NOT NULL DEFAULT 0 CHECK (seeders >= 0),
    has_seeders INTEGER NOT NULL DEFAULT 0 CHECK (has_seeders IN (0, 1)),
    outcome TEXT NOT NULL CHECK (outcome IN ('pending', 'selected', 'stalled', 'missing', 'unplayable')),
    PRIMARY KEY (job_id, ordinal),
    UNIQUE (job_id, info_hash)
);

CREATE UNIQUE INDEX acquisition_job_candidates_one_selected
    ON acquisition_job_candidates (job_id)
    WHERE outcome = 'selected';
