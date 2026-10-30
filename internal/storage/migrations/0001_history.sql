CREATE TABLE {{schema}}.collection_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 property_id text NOT NULL CHECK (length(btrim(property_id)) BETWEEN 1 AND 128),
 period_start date NOT NULL,
 period_end date NOT NULL CHECK (period_end > period_start),
 started_at timestamptz NOT NULL,
 finished_at timestamptz,
 status text NOT NULL CHECK (status IN ('running','succeeded','partial','failed','canceled')),
 UNIQUE(id,property_id),
 CHECK ((status = 'running' AND finished_at IS NULL) OR (status <> 'running' AND finished_at >= started_at))
);
CREATE INDEX collection_runs_property_period_idx ON {{schema}}.collection_runs(property_id, period_start, started_at DESC);

CREATE TABLE {{schema}}.scenario_jobs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 scenario_key bytea NOT NULL CHECK (octet_length(scenario_key) = 32),
 property_id text NOT NULL,
 source text NOT NULL CHECK (length(btrim(source)) BETWEEN 1 AND 128),
 source_origin text NOT NULL CHECK (source_origin ~ '^https?://[^/@?#]+$'),
 source_property_id text NOT NULL CHECK (length(btrim(source_property_id)) BETWEEN 1 AND 256),
 check_in date NOT NULL,
 check_out date NOT NULL,
 los integer NOT NULL CHECK (los > 0 AND los = check_out - check_in),
 adults integer NOT NULL CHECK (adults > 0),
 rooms integer NOT NULL CHECK (rooms > 0),
 children_ages integer[] NOT NULL CHECK (0 <= ALL(children_ages) AND 18 > ALL(children_ages) AND array_position(children_ages,NULL) IS NULL),
 language text NOT NULL CHECK (length(btrim(language)) BETWEEN 1 AND 16),
 status text NOT NULL CHECK (status IN ('queued','running','succeeded','partial','no_offers','failed','retryable','canceled')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 last_error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 started_at timestamptz,
 finished_at timestamptz,
 next_retry_at timestamptz,
 UNIQUE (run_id, scenario_key),
 FOREIGN KEY(run_id,property_id) REFERENCES {{schema}}.collection_runs(id,property_id) ON DELETE RESTRICT,
 CHECK (finished_at IS NULL OR started_at IS NULL OR finished_at >= started_at),
 CHECK ((status = 'retryable' AND next_retry_at IS NOT NULL) OR (status <> 'retryable' AND next_retry_at IS NULL)),
 CHECK ((status IN ('queued','running','retryable') AND finished_at IS NULL) OR (status IN ('succeeded','partial','no_offers','failed','canceled') AND finished_at IS NOT NULL))
);
CREATE INDEX scenario_jobs_run_idx ON {{schema}}.scenario_jobs(run_id, id);
CREATE INDEX scenario_jobs_pending_idx ON {{schema}}.scenario_jobs(status, next_retry_at, created_at) WHERE status IN ('queued','retryable');
CREATE INDEX scenario_jobs_identity_idx ON {{schema}}.scenario_jobs(property_id, source, check_in, check_out, adults);

CREATE TABLE {{schema}}.collection_attempts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 job_id uuid NOT NULL REFERENCES {{schema}}.scenario_jobs(id) ON DELETE RESTRICT,
 attempt_no integer NOT NULL CHECK (attempt_no > 0),
 payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
 started_at timestamptz NOT NULL,
 finished_at timestamptz NOT NULL CHECK (finished_at >= started_at),
 duration_ms bigint NOT NULL CHECK (duration_ms >= 0),
 queue_wait_ms bigint CHECK (queue_wait_ms >= 0),
 collected_at timestamptz,
 outcome text NOT NULL CHECK (outcome IN ('success','partial','no_offers','failed')),
 http_status integer NOT NULL CHECK (http_status = 0 OR http_status BETWEEN 100 AND 599),
 error_code text,
 retryable boolean NOT NULL,
 warnings jsonb NOT NULL CHECK (jsonb_typeof(warnings) = 'array'),
 next_retry_at timestamptz,
 UNIQUE(job_id, attempt_no),
 CHECK ((outcome = 'failed' AND error_code IS NOT NULL) OR (outcome <> 'failed' AND error_code IS NULL AND NOT retryable)),
 CHECK (next_retry_at IS NULL OR (retryable AND outcome = 'failed'))
);
CREATE INDEX collection_attempts_time_idx ON {{schema}}.collection_attempts(started_at DESC);
CREATE INDEX collection_attempts_errors_idx ON {{schema}}.collection_attempts(error_code, started_at DESC) WHERE error_code IS NOT NULL;

CREATE TABLE {{schema}}.observations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 attempt_id uuid NOT NULL REFERENCES {{schema}}.collection_attempts(id) ON DELETE RESTRICT,
 category_id text NOT NULL CHECK (length(btrim(category_id)) BETWEEN 1 AND 256),
 category_name text NOT NULL,
 area_m2 numeric CHECK (area_m2 > 0 AND area_m2::text NOT IN ('NaN','Infinity','-Infinity')),
 available integer,
 stock_status text NOT NULL CHECK (stock_status IN ('known','unknown','conflict')),
 has_offers boolean NOT NULL,
 confirmed_unavailable boolean NOT NULL,
 warnings jsonb NOT NULL CHECK (jsonb_typeof(warnings) = 'array'),
 UNIQUE(attempt_id,category_id),
 CHECK ((stock_status = 'known' AND available IS NOT NULL AND available >= 0) OR (stock_status <> 'known' AND available IS NULL))
);
CREATE INDEX observations_category_history_idx ON {{schema}}.observations(category_id, attempt_id);

CREATE TABLE {{schema}}.observation_variants (
 observation_id uuid NOT NULL REFERENCES {{schema}}.observations(id) ON DELETE RESTRICT,
 variant_id text NOT NULL CHECK (length(btrim(variant_id)) BETWEEN 1 AND 256),
 adults integer NOT NULL CHECK (adults >= 0),
 PRIMARY KEY(observation_id, variant_id)
);
CREATE TABLE {{schema}}.observation_offers (
 observation_id uuid NOT NULL,
 variant_id text NOT NULL,
 rate_id text NOT NULL CHECK (length(btrim(rate_id)) BETWEEN 1 AND 256),
 rate_name text NOT NULL,
 price numeric CHECK (price >= 0 AND price::text NOT IN ('NaN','Infinity','-Infinity')),
 currency text CHECK (currency ~ '^[A-Z]{3}$'),
 meal_plan text NOT NULL,
 visible boolean NOT NULL,
 data_available integer CHECK (data_available >= 0),
 row_available boolean,
 data_available_for_guests text,
 select_disabled boolean NOT NULL,
 selectable_quantities integer[] NOT NULL CHECK (0 < ALL(selectable_quantities) AND array_position(selectable_quantities,NULL) IS NULL),
 PRIMARY KEY(observation_id, variant_id, rate_id),
 FOREIGN KEY(observation_id,variant_id) REFERENCES {{schema}}.observation_variants(observation_id,variant_id) ON DELETE RESTRICT
);
CREATE INDEX observation_offers_rate_idx ON {{schema}}.observation_offers(rate_id, observation_id);

CREATE FUNCTION {{schema}}.reject_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'collection history is append-only' USING ERRCODE = '55000';
END;
$$;
CREATE TRIGGER immutable_attempts BEFORE UPDATE OR DELETE ON {{schema}}.collection_attempts FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_observations BEFORE UPDATE OR DELETE ON {{schema}}.observations FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_variants BEFORE UPDATE OR DELETE ON {{schema}}.observation_variants FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_offers BEFORE UPDATE OR DELETE ON {{schema}}.observation_offers FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_attempts_truncate BEFORE TRUNCATE ON {{schema}}.collection_attempts EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_observations_truncate BEFORE TRUNCATE ON {{schema}}.observations EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_variants_truncate BEFORE TRUNCATE ON {{schema}}.observation_variants EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_offers_truncate BEFORE TRUNCATE ON {{schema}}.observation_offers EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_migrations BEFORE UPDATE OR DELETE ON {{schema}}.schema_migrations FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_migrations_truncate BEFORE TRUNCATE ON {{schema}}.schema_migrations EXECUTE FUNCTION {{schema}}.reject_history_mutation();

CREATE FUNCTION {{schema}}.protect_run_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'collection runs cannot be deleted' USING ERRCODE = '55000';
 END IF;
 IF ROW(NEW.id,NEW.property_id,NEW.period_start,NEW.period_end,NEW.started_at)
    IS DISTINCT FROM ROW(OLD.id,OLD.property_id,OLD.period_start,OLD.period_end,OLD.started_at)
    OR (OLD.finished_at IS NOT NULL AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'collection run identity is immutable' USING ERRCODE = '55000';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER immutable_run_identity BEFORE UPDATE OR DELETE ON {{schema}}.collection_runs FOR EACH ROW EXECUTE FUNCTION {{schema}}.protect_run_identity();
CREATE FUNCTION {{schema}}.protect_job_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'scenario jobs cannot be deleted' USING ERRCODE = '55000';
 END IF;
 IF ROW(NEW.id,NEW.run_id,NEW.scenario_key,NEW.property_id,NEW.source,NEW.source_origin,NEW.source_property_id,NEW.check_in,NEW.check_out,NEW.los,NEW.adults,NEW.rooms,NEW.children_ages,NEW.language,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.id,OLD.run_id,OLD.scenario_key,OLD.property_id,OLD.source,OLD.source_origin,OLD.source_property_id,OLD.check_in,OLD.check_out,OLD.los,OLD.adults,OLD.rooms,OLD.children_ages,OLD.language,OLD.created_at)
    OR NEW.attempts < OLD.attempts
    OR (OLD.status IN ('succeeded','partial','no_offers','failed','canceled') AND NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'scenario identity is immutable' USING ERRCODE = '55000';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER immutable_job_identity BEFORE UPDATE OR DELETE ON {{schema}}.scenario_jobs FOR EACH ROW EXECUTE FUNCTION {{schema}}.protect_job_identity();
CREATE TRIGGER immutable_runs_truncate BEFORE TRUNCATE ON {{schema}}.collection_runs EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_jobs_truncate BEFORE TRUNCATE ON {{schema}}.scenario_jobs EXECUTE FUNCTION {{schema}}.reject_history_mutation();
