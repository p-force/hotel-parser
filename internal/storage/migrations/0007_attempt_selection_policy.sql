-- New evidence only: do not backfill historical attempts from current configs.
CREATE TABLE {{schema}}.attempt_selection_policy (
 attempt_id uuid PRIMARY KEY REFERENCES {{schema}}.collection_attempts(id),
 policy jsonb NOT NULL CHECK (jsonb_typeof(policy) = 'object')
);
CREATE TRIGGER immutable_attempt_selection_policy BEFORE UPDATE OR DELETE ON {{schema}}.attempt_selection_policy
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_attempt_selection_policy_truncate BEFORE TRUNCATE ON {{schema}}.attempt_selection_policy
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
