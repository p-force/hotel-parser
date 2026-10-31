-- psql -v schema=hp_test_<your_isolated_schema> -f diagnostics.sql
-- Read-only. Original live/replay schemas were cleaned up; create an isolated run first.
SELECT j.property_id,j.status,count(*) jobs,sum(attempts) attempts
FROM :"schema".scenario_jobs j GROUP BY j.property_id,j.status ORDER BY 1,2;

SELECT 'duplicate_jobs' AS check_name,count(*) FROM (
 SELECT run_id,scenario_key FROM :"schema".scenario_jobs
 GROUP BY run_id,scenario_key HAVING count(*)>1) x
UNION ALL SELECT 'duplicate_attempts',count(*) FROM (
 SELECT job_id,attempt_no FROM :"schema".collection_attempts
 GROUP BY job_id,attempt_no HAVING count(*)>1) x;

SELECT count(*) AS attempt_count_mismatches FROM :"schema".scenario_jobs j
WHERE j.attempts<>(SELECT count(*) FROM :"schema".collection_attempts a WHERE a.job_id=j.id)
 OR j.attempts<>COALESCE((SELECT max(attempt_no) FROM :"schema".collection_attempts a WHERE a.job_id=j.id),0);

SELECT count(*) AS orphan_offers FROM :"schema".observation_offers f
LEFT JOIN :"schema".observation_variants v
 ON v.observation_id=f.observation_id AND v.variant_id=f.variant_id
WHERE v.observation_id IS NULL;

SELECT j.property_id,o.category_id,o.stock_status,o.confirmed_unavailable,
 count(*) observations,min(o.available) min_available,max(o.available) max_available
FROM :"schema".scenario_jobs j
JOIN :"schema".collection_attempts a ON a.job_id=j.id
JOIN :"schema".observations o ON o.attempt_id=a.id
GROUP BY 1,2,3,4 ORDER BY 1,2,3,4;

SELECT r.id,r.property_id,r.started_at,r.finished_at,r.status,
 count(j.id) FILTER(WHERE j.status IN ('queued','running','retryable')) remaining
FROM :"schema".collection_runs r LEFT JOIN :"schema".scenario_jobs j ON j.run_id=r.id
GROUP BY r.id ORDER BY r.property_id;

SELECT count(*) AS future_run_finished FROM :"schema".collection_runs
WHERE finished_at>clock_timestamp();
SELECT count(*) AS invalid_attempt_order FROM :"schema".collection_attempts
WHERE finished_at<started_at;
