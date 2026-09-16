-- Owner: runtime-host. Bundle runners use a verified private bridge address.
-- Legacy image-only workloads retain their loopback-only endpoint contract.
ALTER TABLE workos_runtime.workloads DROP CONSTRAINT workloads_endpoint_check;
ALTER TABLE workos_runtime.workloads ADD CONSTRAINT workloads_endpoint_check CHECK (
    endpoint IS NULL OR (
        endpoint ~ '^[0-9.]+:[1-9][0-9]{0,4}$'
        AND split_part(endpoint, ':', 2)::integer BETWEEN 1 AND 65535
        AND (
            split_part(endpoint, ':', 1) = '127.0.0.1'
            OR (artifact_id <> '' AND artifact_digest <> '' AND (
                split_part(endpoint, ':', 1)::inet << inet '10.0.0.0/8'
                OR split_part(endpoint, ':', 1)::inet << inet '172.16.0.0/12'
                OR split_part(endpoint, ':', 1)::inet << inet '192.168.0.0/16'
            ))
        )
    )
);
