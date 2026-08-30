-- Gateway-owned device auth queries (owner: workos-gateway; ADR-0007).
-- Every query targets the workos_gateway schema exclusively; no query in
-- this package may touch Core, Runtime, or Reliability tables. State
-- transitions are guarded UPDATEs: the caller treats 0 affected rows as a
-- lost race or an expired object, never as silent success.

-- name: AuthStoreReady :one
SELECT 1;

-- name: LockOwnerTicketRotation :exec
SELECT pg_advisory_xact_lock(2087990351, hashtext(sqlc.arg(owner_user_id)::text));

-- name: RevokePendingTickets :execrows
-- The revocation timestamp is the database transaction time: rotations
-- serialized by the owner lock must never stamp a revocation earlier than a
-- ticket a previous lock holder already committed.
UPDATE workos_gateway.pairing_tickets
SET state = 'revoked', revoked_at = now()
WHERE owner_user_id = sqlc.arg(owner_user_id) AND state = 'pending';

-- name: InsertPairingTicket :exec
INSERT INTO workos_gateway.pairing_tickets (
    id, owner_user_id, secret_hash, public_origin, tls_fingerprint,
    state, attempts, expires_at, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(secret_hash),
    sqlc.arg(public_origin), sqlc.arg(tls_fingerprint), 'pending', 0,
    sqlc.arg(expires_at)::timestamptz, sqlc.arg(created_at)::timestamptz
);

-- name: GetTicketBySecretHash :one
SELECT id, owner_user_id, secret_hash, public_origin, tls_fingerprint,
       state, device_id, public_key_hash, claimed_name, claimed_class,
       attempts, expires_at, created_at, claimed_at, completed_at, revoked_at
FROM workos_gateway.pairing_tickets
WHERE secret_hash = sqlc.arg(secret_hash) AND expires_at > sqlc.arg(now)::timestamptz;

-- name: ClaimPairingTicket :one
UPDATE workos_gateway.pairing_tickets
SET state = 'claimed', claimed_at = sqlc.arg(now)::timestamptz,
    device_id = sqlc.arg(device_id), public_key_hash = sqlc.arg(public_key_hash),
    claimed_name = sqlc.arg(claimed_name), claimed_class = sqlc.arg(claimed_class)
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id)
  AND state = 'pending' AND expires_at > sqlc.arg(now)::timestamptz
RETURNING id, owner_user_id, secret_hash, public_origin, tls_fingerprint,
          state, device_id, public_key_hash, claimed_name, claimed_class,
          attempts, expires_at, created_at, claimed_at, completed_at, revoked_at;

-- name: GetPairingTicket :one
SELECT id, owner_user_id, secret_hash, public_origin, tls_fingerprint,
       state, device_id, public_key_hash, claimed_name, claimed_class,
       attempts, expires_at, created_at, claimed_at, completed_at, revoked_at
FROM workos_gateway.pairing_tickets
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id);

-- name: LockTicketForComplete :one
SELECT id, owner_user_id, secret_hash, public_origin, tls_fingerprint,
       state, device_id, public_key_hash, claimed_name, claimed_class,
       attempts, expires_at, created_at, claimed_at, completed_at, revoked_at
FROM workos_gateway.pairing_tickets
WHERE id = sqlc.arg(id) AND state = 'claimed'
FOR UPDATE;

-- name: LockChallenge :one
SELECT id, purpose, device_id, ticket_id, public_key_hash, nonce, attempts,
       expires_at, created_at, consumed_at, consumed_by_device, result
FROM workos_gateway.device_auth_challenges
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- name: CompletePairingTicket :execrows
UPDATE workos_gateway.pairing_tickets
SET state = 'completed', completed_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND state = 'claimed' AND expires_at > sqlc.arg(now)::timestamptz;

-- name: FailTicketAttempt :execrows
UPDATE workos_gateway.pairing_tickets
SET attempts = attempts + 1
WHERE id = sqlc.arg(id) AND attempts < 10;

-- name: InsertChallenge :exec
INSERT INTO workos_gateway.device_auth_challenges (
    id, purpose, device_id, ticket_id, public_key_hash, nonce,
    attempts, expires_at, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(purpose), sqlc.arg(device_id), sqlc.arg(ticket_id),
    sqlc.arg(public_key_hash), sqlc.arg(nonce), 0,
    sqlc.arg(expires_at)::timestamptz, sqlc.arg(created_at)::timestamptz
);

-- name: GetChallenge :one
SELECT id, purpose, device_id, ticket_id, public_key_hash, nonce, attempts,
       expires_at, created_at, consumed_at, consumed_by_device, result
FROM workos_gateway.device_auth_challenges
WHERE id = sqlc.arg(id);

-- name: ConsumeChallenge :execrows
UPDATE workos_gateway.device_auth_challenges
SET consumed_at = sqlc.arg(now)::timestamptz, consumed_by_device = sqlc.arg(device_id),
    result = sqlc.arg(result)::text
WHERE id = sqlc.arg(id) AND consumed_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz;

-- name: FailChallengeAttempt :execrows
UPDATE workos_gateway.device_auth_challenges
SET attempts = attempts + 1
WHERE id = sqlc.arg(id) AND attempts < 10;

-- name: InsertDevice :exec
INSERT INTO workos_gateway.device_credentials (
    id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
    revision, created_at, last_authenticated_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(name), sqlc.arg(device_class),
    sqlc.arg(public_key_spki), sqlc.arg(public_key_hash), 1,
    sqlc.arg(now)::timestamptz, sqlc.arg(now)::timestamptz
);

-- name: GetDevice :one
SELECT id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
       revision, created_at, last_authenticated_at, revoked_at
FROM workos_gateway.device_credentials
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id);

-- name: GetActiveDeviceByID :one
SELECT id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
       revision, created_at, last_authenticated_at, revoked_at
FROM workos_gateway.device_credentials
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: GetActiveDeviceByKeyHash :one
SELECT id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
       revision, created_at, last_authenticated_at, revoked_at
FROM workos_gateway.device_credentials
WHERE owner_user_id = sqlc.arg(owner_user_id) AND public_key_hash = sqlc.arg(public_key_hash)
  AND revoked_at IS NULL;

-- name: TouchDeviceAuthenticated :execrows
UPDATE workos_gateway.device_credentials
SET last_authenticated_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id) AND revoked_at IS NULL;

-- name: ListDevices :many
SELECT id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
       revision, created_at, last_authenticated_at, revoked_at
FROM workos_gateway.device_credentials
WHERE owner_user_id = sqlc.arg(owner_user_id) AND id < sqlc.arg(cursor)::uuid
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: RevokeDeviceCredential :execrows
UPDATE workos_gateway.device_credentials
SET revoked_at = sqlc.arg(now)::timestamptz, revision = revision + 1
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id)
  AND revision = sqlc.arg(expected_revision) AND revoked_at IS NULL;

-- name: LockDeviceByID :one
SELECT id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
       revision, created_at, last_authenticated_at, revoked_at
FROM workos_gateway.device_credentials
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
FOR UPDATE;

-- name: LockDeviceForSession :one
SELECT id, owner_user_id, name, device_class, public_key_spki, public_key_hash,
       revision, created_at, last_authenticated_at, revoked_at
FROM workos_gateway.device_credentials
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id) AND revoked_at IS NULL
FOR UPDATE;

-- name: InsertSession :exec
INSERT INTO workos_gateway.device_sessions (
    id, owner_user_id, device_id, token_hash, created_at, expires_at
) VALUES (
    sqlc.arg(id), sqlc.arg(owner_user_id), sqlc.arg(device_id),
    sqlc.arg(token_hash), sqlc.arg(created_at)::timestamptz, sqlc.arg(expires_at)::timestamptz
);

-- name: GetSessionByTokenHash :one
SELECT id, owner_user_id, device_id, token_hash, created_at, expires_at,
       last_seen_at, revoked_at
FROM workos_gateway.device_sessions
WHERE token_hash = sqlc.arg(token_hash);

-- name: RevokeActiveSessions :execrows
-- Database transaction time, for the same monotonicity reason as ticket
-- rotation: a session a concurrent transaction just committed must never
-- outlive its own creation timestamp.
UPDATE workos_gateway.device_sessions
SET revoked_at = now()
WHERE device_id = sqlc.arg(device_id) AND owner_user_id = sqlc.arg(owner_user_id)
  AND revoked_at IS NULL;

-- name: RevokeSession :execrows
UPDATE workos_gateway.device_sessions
SET revoked_at = now()
WHERE id = sqlc.arg(id) AND owner_user_id = sqlc.arg(owner_user_id) AND revoked_at IS NULL;

-- name: TouchSessionLastSeen :execrows
UPDATE workos_gateway.device_sessions
SET last_seen_at = sqlc.arg(now)::timestamptz
WHERE id = sqlc.arg(id) AND revoked_at IS NULL
  AND (last_seen_at IS NULL OR last_seen_at < sqlc.arg(threshold)::timestamptz);

-- name: GetRevocationRequest :one
SELECT owner_user_id, idempotency_key, request_digest, result, result_version, created_at
FROM workos_gateway.device_revocation_requests
WHERE owner_user_id = sqlc.arg(owner_user_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: InsertRevocationRequest :exec
INSERT INTO workos_gateway.device_revocation_requests (
    owner_user_id, idempotency_key, request_digest, result, result_version, created_at
) VALUES (
    sqlc.arg(owner_user_id), sqlc.arg(idempotency_key), sqlc.arg(request_digest),
    sqlc.arg(result)::jsonb, 'v1', sqlc.arg(created_at)::timestamptz
);
