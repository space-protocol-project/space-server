CREATE TABLE directory_transfers (
    id text PRIMARY KEY,
    owner_hash bytea NOT NULL CHECK (octet_length(owner_hash)=32),
    code_hash bytea UNIQUE CHECK (octet_length(code_hash)=32),
    peer_hash bytea CHECK (octet_length(peer_hash)=32),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    next_sequence integer NOT NULL DEFAULT 0 CHECK (next_sequence BETWEEN 0 AND 80),
    final_sequence integer,
    wire_bytes integer NOT NULL DEFAULT 0 CHECK (wire_bytes BETWEEN 0 AND 5242880)
);
CREATE INDEX directory_transfers_expiry ON directory_transfers(expires_at);
CREATE TABLE directory_transfer_frames (
    transfer_id text NOT NULL REFERENCES directory_transfers(id) ON DELETE CASCADE,
    sequence integer NOT NULL CHECK (sequence BETWEEN 0 AND 79),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 65535),
    final boolean NOT NULL,
    PRIMARY KEY (transfer_id,sequence)
);
