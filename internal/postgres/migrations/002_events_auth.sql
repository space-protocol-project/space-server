CREATE TABLE principals (
    id text PRIMARY KEY,
    root_public_key bytea NOT NULL UNIQUE CHECK (octet_length(root_public_key)=32),
    auth_epoch bigint NOT NULL DEFAULT 1 CHECK (auth_epoch > 0)
);
CREATE TABLE device_grants (
    id text PRIMARY KEY,
    principal_id text NOT NULL REFERENCES principals(id),
    device_public_key bytea NOT NULL CHECK (octet_length(device_public_key)=32),
    auth_epoch bigint NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    registration_transcript bytea NOT NULL,
    root_signature bytea NOT NULL CHECK (octet_length(root_signature)=64)
);
CREATE TABLE auth_challenges (
    id text PRIMARY KEY,
    purpose text NOT NULL CHECK (purpose IN ('device.register','auth.login','device.revoke')),
    transcript bytea NOT NULL,
    verification_key bytea NOT NULL CHECK (octet_length(verification_key)=32),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);
CREATE INDEX auth_challenges_expiry ON auth_challenges(expires_at);
CREATE TABLE auth_sessions (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    grant_id text NOT NULL REFERENCES device_grants(id),
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_sessions_grant ON auth_sessions(grant_id);

ALTER TABLE contents ADD COLUMN author_id text NOT NULL DEFAULT 'legacy-demo';
ALTER TABLE contents DROP CONSTRAINT contents_channel_id_idempotency_key_key;
ALTER TABLE contents ADD UNIQUE (channel_id, author_id, idempotency_key);

CREATE TABLE events (
    channel_id text NOT NULL,
    sequence bigint NOT NULL,
    cursor text NOT NULL,
    type text NOT NULL CHECK (type='content.created'),
    PRIMARY KEY (channel_id, sequence),
    UNIQUE (channel_id, cursor),
    FOREIGN KEY (channel_id, sequence) REFERENCES contents(channel_id, sequence)
);
-- История первого прототипа получает события при обновлении.
INSERT INTO events(channel_id,sequence,cursor,type)
SELECT channel_id,sequence,'event-' || sequence,'content.created' FROM contents;
