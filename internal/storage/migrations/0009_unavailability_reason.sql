-- Existing observations keep NULL; no default or historical backfill.
ALTER TABLE {{schema}}.observations
 ADD COLUMN unavailability_reason text,
 ADD CONSTRAINT observations_unavailability_reason_value_check
  CHECK (unavailability_reason IS NULL OR unavailability_reason = 'booking_forbidden'),
 ADD CONSTRAINT observations_unavailability_reason_state_check
  CHECK (unavailability_reason IS NULL OR (confirmed_unavailable AND NOT has_offers));
