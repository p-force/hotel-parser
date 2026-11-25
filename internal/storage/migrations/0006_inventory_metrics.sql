-- Before 0006 no inventory configuration was persisted: history contains only
-- observed availability, never a recoverable physical/public configured maximum.
-- Do not infer inventory from observations/offers or backfill old runs from today's
-- configuration. No historical tables or migration checksums are changed.
CREATE TABLE {{schema}}.run_inventory (
 run_id uuid PRIMARY KEY REFERENCES {{schema}}.collection_runs(id),
 physical_total_rooms integer CHECK (physical_total_rooms > 0),
 public_observed_max integer CHECK (public_observed_max > 0),
 physical_source text,
 public_source text,
 categories_form_complete_partition boolean NOT NULL,
 CHECK (physical_total_rooms IS NULL OR public_observed_max IS NULL OR public_observed_max <= physical_total_rooms)
);
CREATE TABLE {{schema}}.run_category_inventory (
 run_id uuid NOT NULL REFERENCES {{schema}}.run_inventory(run_id),
 source_room_id text NOT NULL,
 category_id text NOT NULL,
 category_name text NOT NULL,
 position integer NOT NULL CHECK (position >= 0),
 physical_total_rooms integer CHECK (physical_total_rooms > 0),
 public_observed_max integer CHECK (public_observed_max > 0),
 PRIMARY KEY(run_id,source_room_id),
 UNIQUE(run_id,category_id),
 UNIQUE(run_id,position),
 CHECK (physical_total_rooms IS NULL OR public_observed_max IS NULL OR public_observed_max <= physical_total_rooms)
);
CREATE TRIGGER immutable_run_inventory BEFORE UPDATE OR DELETE ON {{schema}}.run_inventory
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_run_inventory_truncate BEFORE TRUNCATE ON {{schema}}.run_inventory
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_category_inventory BEFORE UPDATE OR DELETE ON {{schema}}.run_category_inventory
 FOR EACH ROW EXECUTE FUNCTION {{schema}}.reject_history_mutation();
CREATE TRIGGER immutable_category_inventory_truncate BEFORE TRUNCATE ON {{schema}}.run_category_inventory
 EXECUTE FUNCTION {{schema}}.reject_history_mutation();
