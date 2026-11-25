-- Presence marker preserves [] vs absent evidence; historical attempts are not backfilled.
CREATE TABLE {{schema}}.attempt_room_type_quota_sets (
 attempt_id uuid PRIMARY KEY REFERENCES {{schema}}.collection_attempts(id)
);
CREATE TABLE {{schema}}.attempt_room_type_quotas (
 attempt_id uuid NOT NULL REFERENCES {{schema}}.attempt_room_type_quota_sets(attempt_id),
 ordinal integer NOT NULL CHECK (ordinal >= 0),
 rph text NOT NULL,
 quantity bigint,
 source_room_id text,
 mapping_status text NOT NULL CHECK (mapping_status IN ('mapped','unmapped','conflict')),
 PRIMARY KEY (attempt_id,ordinal),
 CHECK ((mapping_status='mapped' AND source_room_id IS NOT NULL AND source_room_id<>'')
     OR (mapping_status IN ('unmapped','conflict') AND source_room_id IS NULL))
);
CREATE TRIGGER immutable_quota_sets BEFORE UPDATE OR DELETE ON {{schema}}.attempt_room_type_quota_sets
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_quota_sets_truncate BEFORE TRUNCATE ON {{schema}}.attempt_room_type_quota_sets
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_attempt_quotas BEFORE UPDATE OR DELETE ON {{schema}}.attempt_room_type_quotas
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_attempt_quotas_truncate BEFORE TRUNCATE ON {{schema}}.attempt_room_type_quotas
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
