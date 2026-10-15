ALTER TABLE channels ADD COLUMN mls_group_id bytea;
ALTER TABLE channels ADD CHECK (mls_group_id IS NULL OR (octet_length(mls_group_id) BETWEEN 16 AND 256 AND NOT public_preview));
CREATE UNIQUE INDEX channels_mls_group ON channels(mls_group_id) WHERE mls_group_id IS NOT NULL;
CREATE TABLE mls_devices (
    grant_id text PRIMARY KEY REFERENCES device_grants(id),
    binding bytea NOT NULL CHECK(octet_length(binding) BETWEEN 1 AND 8192),
    key_package bytea NOT NULL CHECK(octet_length(key_package) BETWEEN 1 AND 65536),
    consumed boolean NOT NULL DEFAULT false
);
CREATE TABLE mls_channels (
    channel_id text PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    epoch bigint NOT NULL DEFAULT 0 CHECK(epoch >= 0),
    sequence bigint NOT NULL DEFAULT 0 CHECK(sequence >= 0)
);
CREATE TABLE mls_members (
    channel_id text NOT NULL REFERENCES mls_channels(channel_id) ON DELETE CASCADE,
    grant_id text NOT NULL REFERENCES mls_devices(grant_id),
    joined_sequence bigint NOT NULL,
    left_sequence bigint,
    PRIMARY KEY(channel_id,grant_id)
);
CREATE TABLE mls_envelopes (
    channel_id text NOT NULL REFERENCES mls_channels(channel_id) ON DELETE CASCADE,
    sequence bigint NOT NULL,
    request_id bytea NOT NULL CHECK(octet_length(request_id)=16),
    request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
    envelope bytea NOT NULL,
    PRIMARY KEY(channel_id,sequence),
    UNIQUE(channel_id,request_id)
);
ALTER TABLE assets ADD COLUMN mls_sequence bigint;
ALTER TABLE assets ADD FOREIGN KEY(channel_id,mls_sequence) REFERENCES mls_envelopes(channel_id,sequence) ON DELETE CASCADE;
ALTER TABLE assets DROP CONSTRAINT assets_byte_size_check;
ALTER TABLE assets ADD CHECK(byte_size BETWEEN 1 AND 20971536);
