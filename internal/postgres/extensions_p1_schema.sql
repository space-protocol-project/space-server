-- Только явно включаемый прототип, не миграция рабочих пространств.
CREATE SCHEMA IF NOT EXISTS extension_p1;
CREATE TABLE IF NOT EXISTS extension_p1.meta (
 singleton boolean PRIMARY KEY CHECK(singleton), version integer NOT NULL CHECK(version=1)
);
INSERT INTO extension_p1.meta VALUES(true,1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS extension_p1.instances (
 installation text NOT NULL, instance text NOT NULL, channel text NOT NULL,
 package_hash bytea NOT NULL CHECK(octet_length(package_hash)=32),
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 PRIMARY KEY(installation,instance)
);
CREATE TABLE IF NOT EXISTS extension_p1.data (
 installation text NOT NULL, instance text NOT NULL, key text NOT NULL,
 value bytea NOT NULL CHECK(octet_length(value)<=16384),
 PRIMARY KEY(installation,instance,key),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1.instances ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS extension_p1.receipts (
 installation text NOT NULL, instance text NOT NULL, principal text NOT NULL,
 command text NOT NULL, key text NOT NULL, digest bytea NOT NULL CHECK(octet_length(digest)=32),
 response bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(installation,instance,principal,command,key),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1.instances ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS extension_p1.outbox (
 installation text NOT NULL, instance text NOT NULL, revision bigint NOT NULL,
 ordinal integer NOT NULL, event_id text NOT NULL UNIQUE, type text NOT NULL, payload bytea NOT NULL,
 PRIMARY KEY(installation,instance,revision,ordinal),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1.instances ON DELETE CASCADE
);
