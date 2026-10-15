CREATE TABLE assets (
    id text PRIMARY KEY,
    channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author_id text NOT NULL REFERENCES principals(id),
    name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 255),
    byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 20971520),
    sha256 bytea NOT NULL CHECK (octet_length(sha256)=32),
    media_type text NOT NULL DEFAULT 'application/octet-stream',
    created_at_unix_ms bigint NOT NULL,
    storage_group text NOT NULL,
    idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0 AND received_bytes <= byte_size),
    ready boolean NOT NULL DEFAULT false,
    content_id text,
    UNIQUE(channel_id,author_id,idempotency_key),
    FOREIGN KEY(channel_id,content_id) REFERENCES contents(channel_id,id) ON DELETE CASCADE
);
CREATE INDEX assets_author_date ON assets(channel_id,author_id,created_at_unix_ms DESC,id DESC);
CREATE INDEX assets_pending_date ON assets(created_at_unix_ms) WHERE content_id IS NULL;
CREATE TABLE asset_chunks (
    asset_id text NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    byte_offset bigint NOT NULL CHECK (byte_offset >= 0),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 262144),
    PRIMARY KEY(asset_id,byte_offset)
);

ALTER TABLE contents ADD COLUMN attachments jsonb NOT NULL DEFAULT '[]';
ALTER TABLE contents DROP CONSTRAINT contents_check;
ALTER TABLE contents ADD CHECK (
 (deleted AND text='' AND attachments='[]'::jsonb) OR
 (NOT deleted AND octet_length(text)<=4096 AND (octet_length(text)>0 OR jsonb_array_length(attachments)>0))
);

ALTER TABLE space_settings ADD COLUMN storage_limit_bytes bigint NOT NULL DEFAULT 1073741824 CHECK(storage_limit_bytes BETWEEN 0 AND 1099511627776);
ALTER TABLE space_settings ADD COLUMN member_storage_limit_bytes bigint NOT NULL DEFAULT 1073741824 CHECK(member_storage_limit_bytes BETWEEN 0 AND 1099511627776);

ALTER TABLE memberships ADD COLUMN storage_limit_bytes bigint CHECK(storage_limit_bytes BETWEEN 0 AND 1099511627776);

ALTER TABLE memberships ADD COLUMN nickname text NOT NULL DEFAULT '' CHECK(octet_length(nickname)<=160);
