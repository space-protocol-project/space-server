CREATE TABLE community_reaction_targets (
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 kind int NOT NULL CHECK (kind BETWEEN 1 AND 4),
 target_id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
 PRIMARY KEY(channel_id,kind,target_id)
);
CREATE TABLE community_reactions (
 channel_id text NOT NULL,
 kind int NOT NULL,
 target_id text NOT NULL,
 emoji text NOT NULL CHECK (octet_length(emoji) BETWEEN 1 AND 128),
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 PRIMARY KEY(channel_id,kind,target_id,emoji,principal_id),
 FOREIGN KEY(channel_id,kind,target_id) REFERENCES community_reaction_targets ON DELETE CASCADE
);
CREATE TABLE community_reaction_requests (
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
 request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
 applied_revision bigint NOT NULL CHECK (applied_revision>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(principal_id,channel_id,idempotency_key)
);
CREATE INDEX community_reaction_request_budget ON community_reaction_requests(principal_id,created_at);
