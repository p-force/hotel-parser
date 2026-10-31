-- Read-only evidence queries; recorded hp_test_* schema was removed by test cleanup.
-- attempt_count_mismatches
SELECT to_json(count(*)) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j WHERE j.attempts<>(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a WHERE a.job_id=j.id) OR j.attempts<>COALESCE((SELECT max(attempt_no) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a WHERE a.job_id=j.id),0);

-- attempt_timing
SELECT json_agg(x) FROM(SELECT j.property_id,avg(a.duration_ms) mean_duration_ms,max(a.duration_ms) max_duration_ms,avg(a.queue_wait_ms) mean_pool_queue_wait_ms,max(a.queue_wait_ms) max_pool_queue_wait_ms,avg(EXTRACT(EPOCH FROM(a.started_at-j.created_at))*1000) mean_durable_wait_ms FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a ON a.job_id=j.id GROUP BY j.property_id)x;

-- bad_public_zeros
SELECT json_build_object('known_zero_without_confirmation',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" WHERE stock_status='known' AND available=0 AND (NOT confirmed_unavailable OR has_offers)),'unknown_with_quantity',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" WHERE stock_status IN('unknown','conflict') AND available IS NOT NULL));

-- category_counts
SELECT json_agg(x) FROM(SELECT j.property_id,o.category_id,count(*) observations,count(*) FILTER(WHERE o.stock_status='known') known,count(*) FILTER(WHERE o.stock_status='unknown') unknown,count(*) FILTER(WHERE o.stock_status='conflict') conflict FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" o JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a ON a.id=o.attempt_id JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j ON j.id=a.job_id GROUP BY j.property_id,o.category_id ORDER BY j.property_id,o.category_id)x;

-- counts_by_property
SELECT json_agg(x) FROM (SELECT j.property_id,count(DISTINCT j.id) jobs,count(DISTINCT a.id) attempts,count(DISTINCT o.id) observations,count(f.rate_id) offers,count(DISTINCT j.id) FILTER(WHERE j.status='succeeded') succeeded,count(DISTINCT j.id) FILTER(WHERE j.status='partial') partial,count(DISTINCT j.id) FILTER(WHERE j.status='no_offers') no_offers,count(DISTINCT j.id) FILTER(WHERE j.status='failed') failed,count(DISTINCT j.id) FILTER(WHERE j.status IN('queued','running','retryable')) remaining FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a ON a.job_id=j.id LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" o ON o.attempt_id=a.id LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observation_offers" f ON f.observation_id=o.id GROUP BY j.property_id)x;

-- duplicate_observation_keys
SELECT json_build_object('observations',(SELECT count(*) FROM(SELECT attempt_id,category_id FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" GROUP BY attempt_id,category_id HAVING count(*)>1)x),'variants',(SELECT count(*) FROM(SELECT observation_id,variant_id FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observation_variants" GROUP BY observation_id,variant_id HAVING count(*)>1)x),'offers',(SELECT count(*) FROM(SELECT observation_id,variant_id,rate_id FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observation_offers" GROUP BY observation_id,variant_id,rate_id HAVING count(*)>1)x));

-- duplicates
SELECT json_build_object('jobs',(SELECT count(*) FROM(SELECT run_id,scenario_key FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" GROUP BY run_id,scenario_key HAVING count(*)>1)x),'attempts',(SELECT count(*) FROM(SELECT job_id,attempt_no FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" GROUP BY job_id,attempt_no HAVING count(*)>1)x));

-- exact_date_unavailability
SELECT json_agg(x) FROM(SELECT j.property_id,j.source_property_id,j.check_in,j.check_out,j.los,j.adults,j.status,o.category_id,o.stock_status,o.available,o.confirmed_unavailable,o.has_offers FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a ON a.job_id=j.id JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" o ON o.attempt_id=a.id WHERE j.check_in='2026-12-30' ORDER BY j.property_id,o.category_id)x;

-- http_codes
SELECT COALESCE(json_agg(x),'[]') FROM (SELECT http_status,error_code,count(*) AS attempts FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".collection_attempts GROUP BY http_status,error_code ORDER BY http_status,error_code) x;

-- observed_data
SELECT json_agg(x) FROM(SELECT stock_status,confirmed_unavailable,count(*) observations,count(*) FILTER(WHERE available IS NULL) null_available,count(*) FILTER(WHERE available=0) zero_available,count(*) FILTER(WHERE area_m2 IS NULL) null_area FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" GROUP BY stock_status,confirmed_unavailable)x;

-- orphans
SELECT json_build_object('jobs',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_runs" r ON r.id=j.run_id AND r.property_id=j.property_id WHERE r.id IS NULL),'attempts',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j ON j.id=a.job_id WHERE j.id IS NULL),'observations',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observations" o LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a ON a.id=o.attempt_id WHERE a.id IS NULL),'offers',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observation_offers" f LEFT JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."observation_variants" v ON v.observation_id=f.observation_id AND v.variant_id=f.variant_id WHERE v.observation_id IS NULL));

-- property_jobs
SELECT COALESCE(json_agg(x),'[]') FROM (SELECT property_id,status,count(*) AS jobs,sum(attempts) AS attempts FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".scenario_jobs GROUP BY property_id,status ORDER BY property_id,status) x;

-- runs
SELECT json_agg(x) FROM(SELECT property_id,status,period_start,period_end,started_at,finished_at FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_runs" ORDER BY property_id)x;

-- scenario_shape
SELECT json_agg(x) FROM(SELECT adults,rooms,los,language,min(check_in) first_check_in,max(check_in) last_check_in,count(*) jobs FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" GROUP BY adults,rooms,los,language)x;

-- server_version
SELECT json_build_object('version',version());

-- stock_status
SELECT COALESCE(json_agg(x),'[]') FROM (SELECT stock_status,confirmed_unavailable,count(*) AS observations FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".observations GROUP BY stock_status,confirmed_unavailable) x;

-- table_counts
SELECT json_build_object('runs',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".collection_runs),'jobs',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".scenario_jobs),'attempts',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".collection_attempts),'observations',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".observations),'variants',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".observation_variants),'offers',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf".observation_offers));

-- timestamps
SELECT json_build_object('future_run_finished',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_runs" WHERE finished_at>clock_timestamp()),'bad_attempt_order',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" WHERE finished_at<started_at),'future_attempts',(SELECT count(*) FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" WHERE started_at>clock_timestamp() OR finished_at>clock_timestamp()));

-- warnings
SELECT json_agg(x) FROM(SELECT j.property_id,j.check_in,a.outcome,a.warnings FROM "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."scenario_jobs" j JOIN "hp_test_lmol7khmh2zur4pvwxtunbg2rf"."collection_attempts" a ON a.job_id=j.id WHERE a.warnings<>'[]'::jsonb ORDER BY j.property_id,j.check_in)x;
