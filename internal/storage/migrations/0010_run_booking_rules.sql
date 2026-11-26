CREATE TABLE {{schema}}.run_booking_rules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL UNIQUE REFERENCES {{schema}}.collection_runs(id) ON DELETE RESTRICT,
 requested_at timestamptz NOT NULL,
 received_at timestamptz NOT NULL,
 start_date date NOT NULL,
 end_date date NOT NULL,
 requested_rate_ids text[] NOT NULL
);

CREATE TABLE {{schema}}.booking_rules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 booking_rules_id uuid NOT NULL REFERENCES {{schema}}.run_booking_rules(id) ON DELETE RESTRICT,
 date date NOT NULL,
 room_type_id text,
 rate_id text NOT NULL,
 forbidden boolean,
 min_los integer,
 max_los integer,
 min_adv_booking integer
);
CREATE INDEX booking_rules_lookup_idx ON {{schema}}.booking_rules(booking_rules_id,date,room_type_id,rate_id);

CREATE TRIGGER immutable_run_booking_rules BEFORE UPDATE OR DELETE ON {{schema}}.run_booking_rules
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_run_booking_rules_truncate BEFORE TRUNCATE ON {{schema}}.run_booking_rules
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_booking_rules BEFORE UPDATE OR DELETE ON {{schema}}.booking_rules
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_booking_rules_truncate BEFORE TRUNCATE ON {{schema}}.booking_rules
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
