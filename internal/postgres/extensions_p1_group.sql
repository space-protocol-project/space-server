CREATE TABLE extension_p1_group_jobs (
 installation text PRIMARY KEY REFERENCES extension_p1_installations(installation),
 digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
 record bytea NOT NULL CHECK (octet_length(record)<=40000000),
 record_hash text NOT NULL,
 published boolean NOT NULL DEFAULT false
);
CREATE TABLE extension_p1_group_versions (
 installation text NOT NULL REFERENCES extension_p1_installations(installation),
 sequence bigint NOT NULL CHECK(sequence>0),
 digest text NOT NULL,
 revision bigint NOT NULL,
 principal text NOT NULL,
 generation text NOT NULL,
 record bytea NOT NULL CHECK (octet_length(record)<=40000000),
 record_hash text NOT NULL,
 PRIMARY KEY(installation,sequence),
 UNIQUE(installation,digest),
 UNIQUE(installation,revision)
);
UPDATE extension_p1_meta SET group_version=1 WHERE singleton;
