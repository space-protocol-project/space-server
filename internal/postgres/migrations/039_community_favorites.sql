CREATE TABLE community_favorites (
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 kind int NOT NULL CHECK (kind BETWEEN 1 AND 4),
 target_id text NOT NULL,
 saved boolean NOT NULL,
 revision bigint NOT NULL CHECK (revision > 0),
 saved_at bigint NOT NULL CHECK (saved_at >= 0),
 PRIMARY KEY(principal_id,channel_id,kind,target_id)
);
CREATE INDEX community_favorites_page ON community_favorites(principal_id,channel_id,saved_at DESC,kind DESC,target_id COLLATE "C" DESC) WHERE saved;
CREATE TABLE community_favorite_requests (
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
 request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
 applied_revision bigint NOT NULL CHECK (applied_revision>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(principal_id,channel_id,idempotency_key)
);
CREATE INDEX community_favorite_request_budget ON community_favorite_requests(principal_id,created_at);
