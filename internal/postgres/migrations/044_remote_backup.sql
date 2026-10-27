CREATE TABLE remote_backup_slots (
 id text PRIMARY KEY,
 owner_id text NOT NULL REFERENCES principals(id),
 read_hash bytea NOT NULL CHECK(octet_length(read_hash)=32),
 write_hash bytea NOT NULL CHECK(octet_length(write_hash)=32),
 witness boolean NOT NULL,
 revoked boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 0,
 digest text NOT NULL DEFAULT '',
 parent text NOT NULL DEFAULT '',
 archive_digest text NOT NULL DEFAULT ''
);
CREATE TABLE remote_backup_versions (
 slot_id text NOT NULL REFERENCES remote_backup_slots(id),
 revision bigint NOT NULL,
 digest text NOT NULL,
 parent text NOT NULL,
 archive_digest text NOT NULL,
 archive bytea NOT NULL CHECK(octet_length(archive) BETWEEN 1 AND 5242880),
 PRIMARY KEY(slot_id,revision)
);
