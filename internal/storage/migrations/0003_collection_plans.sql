ALTER TABLE {{schema}}.scenario_jobs ADD COLUMN last_claimed_at timestamptz;
CREATE TABLE {{schema}}.collection_plans (
 run_id uuid PRIMARY KEY REFERENCES {{schema}}.collection_runs(id),
 plan_hash bytea NOT NULL CHECK (octet_length(plan_hash)=32)
);
CREATE TRIGGER immutable_plans BEFORE UPDATE OR DELETE ON {{schema}}.collection_plans
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_plans_truncate BEFORE TRUNCATE ON {{schema}}.collection_plans
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
