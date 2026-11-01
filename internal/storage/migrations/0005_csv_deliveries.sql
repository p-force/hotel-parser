-- Намерение доставки восстанавливается INSERT ... SELECT из terminal runs.
CREATE TABLE {{schema}}.csv_deliveries (
    group_id text NOT NULL,
    run_id uuid NOT NULL REFERENCES {{schema}}.collection_runs(id),
    remote_path text NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','in_progress','delivered','retryable_failed')),
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    leased_until timestamptz,
    error_code text NOT NULL DEFAULT '',
    delivered_at timestamptz,
    PRIMARY KEY(group_id,run_id),
    UNIQUE(remote_path)
);
CREATE INDEX csv_deliveries_due ON {{schema}}.csv_deliveries(next_attempt_at) WHERE state <> 'delivered';
