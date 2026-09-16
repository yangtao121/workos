-- Owner: runtime-host; each interactive module owns its own restart receipts.
ALTER TABLE workos_runtime.pty_sessions ADD COLUMN generation bigint NOT NULL DEFAULT 1 CHECK(generation>0);
ALTER TABLE workos_runtime.native_sessions ADD COLUMN generation bigint NOT NULL DEFAULT 1 CHECK(generation>0);
CREATE TABLE workos_runtime.pty_session_restarts (
 session_id uuid NOT NULL REFERENCES workos_runtime.pty_sessions(session_id),
 action_key text NOT NULL CHECK(length(action_key) BETWEEN 1 AND 128),
 generation bigint NOT NULL,
 PRIMARY KEY(session_id,action_key)
);
CREATE TABLE workos_runtime.native_session_restarts (
 session_id uuid NOT NULL REFERENCES workos_runtime.native_sessions(session_id),
 action_key text NOT NULL CHECK(length(action_key) BETWEEN 1 AND 128),
 generation bigint NOT NULL,
 PRIMARY KEY(session_id,action_key)
);

CREATE TABLE workos_runtime.pty_session_stops (
 session_id uuid NOT NULL REFERENCES workos_runtime.pty_sessions(session_id),
 action_key text NOT NULL CHECK(length(action_key) BETWEEN 1 AND 128),
 generation bigint NOT NULL,
 PRIMARY KEY(session_id,action_key)
);

CREATE TABLE workos_runtime.native_session_stops (
 session_id uuid NOT NULL REFERENCES workos_runtime.native_sessions(session_id),
 action_key text NOT NULL CHECK(length(action_key) BETWEEN 1 AND 128),
 generation bigint NOT NULL,
 PRIMARY KEY(session_id,action_key)
);
