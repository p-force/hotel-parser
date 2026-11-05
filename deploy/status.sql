-- psql -v schema=hotel_parser_arena33 -f deploy/status.sql
-- Run once per configured group schema. Identifiers are quoted by psql.
SET search_path TO :"schema", pg_catalog;
WITH expected(property_id) AS (
 VALUES ('arena33'), ('monarch'), ('bega'), ('azimut_aerostar'), ('sovietsky'), ('petroff')
), latest AS (
 SELECT DISTINCT ON (property_id) * FROM collection_runs
 ORDER BY property_id,started_at DESC,id
), finished AS (
 SELECT property_id,max(finished_at) AS last_finished,
 max(finished_at) FILTER (WHERE status='succeeded') AS last_success
 FROM collection_runs GROUP BY property_id
), jobs AS (
 SELECT run_id,count(*) AS planned,
 count(*) FILTER (WHERE status IN ('queued','running','retryable')) AS remaining,
 count(*) FILTER (WHERE status='partial') AS partial,
 count(*) FILTER (WHERE status='failed') AS failed,
 max(last_claimed_at) AS last_claim,
 count(*) FILTER (WHERE status='running' AND leased_until<clock_timestamp()) AS expired_leases
 FROM scenario_jobs GROUP BY run_id
), backlog AS (
 SELECT property_id,count(*) FILTER (WHERE status IN ('queued','running','retryable')) AS remaining_all_runs,
 max(last_claimed_at) AS last_claim_all_runs FROM scenario_jobs GROUP BY property_id
), due AS (
 SELECT max(slot) AS last_due FROM (
 SELECT ((date_trunc('day',clock_timestamp() AT TIME ZONE 'Europe/Moscow')
         - day * interval '1 day') + hour * interval '1 hour') AT TIME ZONE 'Europe/Moscow' AS slot
 FROM (VALUES (0),(1)) d(day) CROSS JOIN (VALUES (8),(12),(16),(20)) h(hour)
 ) slots WHERE slot<=clock_timestamp()
)
SELECT e.property_id,r.id AS latest_run,r.started_at AS last_planned,f.last_success,f.last_finished,
 r.status,r.finished_at,j.planned,j.remaining,j.partial,j.failed,b.remaining_all_runs,j.expired_leases,
 CASE WHEN r.finished_at IS NOT NULL AND j.remaining=0 THEN 'reconstructable_from_postgres'
      ELSE 'not_final' END AS csv_state,
 d.state AS delivery_state,d.remote_path,d.delivered_at,d.error_code,d.next_attempt_at,
 (r.id IS NULL OR r.started_at<due.last_due) AS missing_current_slot,
 (b.remaining_all_runs>0 AND (b.last_claim_all_runs IS NULL OR
 b.last_claim_all_runs<clock_timestamp()-interval '5 minutes')) AS backlog_without_recent_claim
FROM expected e LEFT JOIN latest r USING(property_id)
LEFT JOIN finished f USING(property_id) LEFT JOIN jobs j ON j.run_id=r.id
LEFT JOIN backlog b USING(property_id)
LEFT JOIN csv_deliveries d ON d.run_id=r.id CROSS JOIN due ORDER BY e.property_id;

-- Older pending deliveries must not disappear behind the latest run.
SELECT r.property_id,r.started_at,d.state,d.attempts,d.error_code,d.next_attempt_at,d.leased_until,d.remote_path
FROM csv_deliveries d JOIN collection_runs r ON r.id=d.run_id
WHERE d.state<>'delivered' ORDER BY r.started_at;
