-- 018: runtime-host workload convergence facts
-- (owner: runtime-host Workload Manager; ADR-0006).
--
-- idle_since is the durable start of the current no-surface interval. It is
-- deliberately separate from updated_at, whose value also changes for
-- lifecycle transitions and therefore cannot safely anchor idle eviction.
-- The historical baseline_pids_events_peak column is retained for migration
-- compatibility, but now stores the pids.events `max` counter. The neutral
-- protocol exposes the corrected name additively.

ALTER TABLE workos_runtime.workloads
    ADD COLUMN idle_since timestamptz;

ALTER TABLE workos_runtime.workload_operations
    DROP CONSTRAINT workload_operations_error_kind_check,
    ADD CONSTRAINT workload_operations_error_kind_check CHECK (
        error_kind IN ('invalid', 'unsupported', 'conflict', 'limit_exhausted',
                       'unavailable', 'failed', 'permanent')
    );
