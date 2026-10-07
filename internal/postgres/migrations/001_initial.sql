CREATE TABLE server_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    server_id text NOT NULL UNIQUE,
    signing_seed bytea NOT NULL CHECK (octet_length(signing_seed) = 32),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE channels (
    id text PRIMARY KEY,
    next_sequence bigint NOT NULL DEFAULT 1 CHECK (next_sequence > 0)
);
INSERT INTO channels (id) VALUES ('general');

CREATE TABLE contents (
    channel_id text NOT NULL REFERENCES channels(id),
    sequence bigint NOT NULL CHECK (sequence > 0),
    id text NOT NULL,
    text text NOT NULL CHECK (octet_length(text) BETWEEN 1 AND 4096),
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, sequence),
    UNIQUE (channel_id, id),
    UNIQUE (channel_id, idempotency_key)
);
