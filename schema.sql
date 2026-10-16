-- The tables jet-admin needs: the admin users and their sessions (used by
-- the auth package) and the log of what was added, changed and deleted
-- (Config.AuditLog). Paste this into one of your own migrations; it needs
-- PostgreSQL 13 or later for gen_random_uuid().

CREATE TABLE admin_users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text NOT NULL UNIQUE CHECK (username <> ''),
    password_hash text NOT NULL,
    is_active     boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz
);

-- A session is the SHA-256 of the cookie's random token, so a copy of the
-- table does not sign anyone in.
CREATE TABLE admin_sessions (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX admin_sessions_user_id ON admin_sessions (user_id);

-- One row per add (1), change (2) or delete (3) made through the admin.
-- change_message names the fields that changed, never their values.
-- username is kept beside user_id so an entry still says who acted after
-- the user is deleted, or when the host signs admins in with its own
-- Authenticator (user_id is then NULL).
CREATE TABLE admin_log (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    action_time    timestamptz NOT NULL DEFAULT now(),
    user_id        uuid REFERENCES admin_users (id) ON DELETE SET NULL,
    username       text NOT NULL DEFAULT '',
    model          text NOT NULL,
    object_id      text NOT NULL,
    object_repr    text NOT NULL,
    action         smallint NOT NULL CHECK (action IN (1, 2, 3)),
    change_message text NOT NULL DEFAULT ''
);

CREATE INDEX admin_log_action_time ON admin_log (action_time DESC);
CREATE INDEX admin_log_object ON admin_log (model, object_id, action_time DESC);
