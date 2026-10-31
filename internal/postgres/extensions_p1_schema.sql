-- Только явно включаемый прототип, не миграция рабочих пространств.
CREATE TABLE IF NOT EXISTS extension_p1_meta (
 singleton boolean PRIMARY KEY CHECK(singleton), version integer NOT NULL CHECK(version=3),
 history_id text NOT NULL CHECK(history_id ~ '^[0-9a-f]{32}$')
);
INSERT INTO extension_p1_meta(singleton,version,history_id) SELECT true,3,replace(gen_random_uuid()::text,'-','') WHERE NOT EXISTS(SELECT 1 FROM extension_p1_meta);
CREATE TABLE IF NOT EXISTS extension_p1_instances (
 installation text NOT NULL, instance text NOT NULL, channel text NOT NULL,
 package_hash bytea NOT NULL CHECK(octet_length(package_hash)=32),
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 PRIMARY KEY(installation,instance)
);
CREATE TABLE IF NOT EXISTS extension_p1_data (
 installation text NOT NULL, instance text NOT NULL, key text NOT NULL,
 value bytea NOT NULL CHECK(octet_length(value)<=16384),
 PRIMARY KEY(installation,instance,key),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1_instances ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS extension_p1_receipts (
 installation text NOT NULL, instance text NOT NULL, principal text NOT NULL,
 command text NOT NULL, key text NOT NULL, digest bytea NOT NULL CHECK(octet_length(digest)=32),
 response bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 retired boolean NOT NULL DEFAULT false,
 PRIMARY KEY(installation,instance,principal,command,key),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1_instances ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS extension_p1_outbox (
 installation text NOT NULL, instance text NOT NULL, revision bigint NOT NULL,
 ordinal integer NOT NULL, event_id text NOT NULL UNIQUE, type text NOT NULL, payload bytea NOT NULL,
 PRIMARY KEY(installation,instance,revision,ordinal),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1_instances ON DELETE CASCADE
);
