ALTER TABLE {{schema}}.scenario_jobs
 ADD COLUMN lease_owner text,
 ADD COLUMN lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token >= 0),
 ADD COLUMN leased_until timestamptz,
 ADD COLUMN max_claims integer NOT NULL DEFAULT 3 CHECK (max_claims BETWEEN 1 AND 1000),
 ADD COLUMN max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 1000),
 ADD CONSTRAINT job_lease_consistent CHECK (
  (lease_token=0 AND lease_owner IS NULL AND leased_until IS NULL)
  OR (lease_token>0 AND lease_owner IS NOT NULL AND length(btrim(lease_owner)) BETWEEN 1 AND 128 AND
      ((status IN ('running','retryable') AND leased_until IS NOT NULL)
       OR (status IN ('succeeded','partial','no_offers','failed','canceled','retryable') AND leased_until IS NULL)))
 );
ALTER TABLE {{schema}}.collection_attempts
 ADD COLUMN lease_owner text,
 ADD COLUMN lease_token bigint NOT NULL DEFAULT 0 CHECK (lease_token>=0),
 ADD COLUMN request_hash bytea CHECK (request_hash IS NULL OR octet_length(request_hash)=32),
 ADD CONSTRAINT attempt_lease_consistent CHECK (
  (lease_token=0 AND lease_owner IS NULL)
  OR (lease_token>0 AND lease_owner IS NOT NULL AND length(btrim(lease_owner)) BETWEEN 1 AND 128)
 );
CREATE INDEX scenario_jobs_expired_lease_idx ON {{schema}}.scenario_jobs(leased_until,id)
 WHERE status IN ('running','retryable') AND leased_until IS NOT NULL;

-- Не меняем историю 0001: новые guards добавляются следующей миграцией.
CREATE FUNCTION {{schema}}.protect_lease_generation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.lease_token < OLD.lease_token OR NEW.lease_token > OLD.lease_token+1
    OR (OLD.lease_token>0 AND (NEW.max_claims<>OLD.max_claims OR NEW.max_attempts<>OLD.max_attempts))
    OR (NEW.lease_owner IS DISTINCT FROM OLD.lease_owner AND NEW.lease_token=OLD.lease_token) THEN
  RAISE EXCEPTION 'invalid lease generation change' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER immutable_lease_generation BEFORE UPDATE ON {{schema}}.scenario_jobs
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.protect_lease_generation();
