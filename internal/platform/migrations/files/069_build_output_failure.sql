-- 069_build_output_failure.sql
-- Owner: runtime-host Build/Test executor (ADR-0033). The docker engine adds
-- a terminal verify-stage failure for jobs whose declared build output cannot
-- be frozen (missing directory, invalid bundle content, quota): build/test
-- passing alone is never a deployable success.
ALTER TABLE workos_runtime.build_jobs
    DROP CONSTRAINT build_jobs_failure_reason_check,
    ADD CONSTRAINT build_jobs_failure_reason_check
        CHECK (failure_reason IS NULL OR failure_reason IN
               ('build-failed', 'test-failed', 'timeout', 'engine-failed',
                'output-budget', 'output-failed', 'input-drift', 'cancelled'));
