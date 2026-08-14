UPDATE watchlist_queue
SET state = 'pending',
    background_job_id = '',
    lease_until_unix_ms = 0,
    next_attempt_unix_ms = 0,
    updated_unix_ms = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE auto_eligible = 0
  AND state = 'acquiring';
