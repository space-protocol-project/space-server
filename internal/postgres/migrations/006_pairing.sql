CREATE TABLE device_pairings (
    id text PRIMARY KEY,
    code_hash bytea UNIQUE CHECK (octet_length(code_hash)=32),
    poll_hash bytea UNIQUE CHECK (octet_length(poll_hash)=32),
    device_public_key bytea NOT NULL CHECK (octet_length(device_public_key)=32),
    device_name text NOT NULL,
    administrative boolean NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','claimed','cancelled')),
    grant_id text UNIQUE REFERENCES device_grants(id)
    ,proposed_root_public_key bytea CHECK (octet_length(proposed_root_public_key)=32)
);
CREATE INDEX device_pairings_expiry ON device_pairings(expires_at);
