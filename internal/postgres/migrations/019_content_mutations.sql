CREATE TABLE content_mutations (
    channel_id text NOT NULL,
    author_id text NOT NULL,
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
    content_id text NOT NULL,
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    applied_revision bigint NOT NULL CHECK (applied_revision > 0),
    PRIMARY KEY (channel_id, author_id, idempotency_key),
    FOREIGN KEY (channel_id, content_id) REFERENCES contents(channel_id, id)
);
