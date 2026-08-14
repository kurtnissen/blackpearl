ALTER TABLE acquisition_jobs
    ADD COLUMN selected_validator TEXT NOT NULL DEFAULT '';

ALTER TABLE acquisition_job_candidates
    ADD COLUMN validator TEXT NOT NULL DEFAULT '';
