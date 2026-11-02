CREATE TABLE extension_p1_versions (
 installation text NOT NULL REFERENCES extension_p1_installations(installation),
 sequence bigint NOT NULL CHECK(sequence>0),
 digest text NOT NULL CHECK(digest ~ '^[0-9a-f]{64}$'),
 source text NOT NULL CHECK(source ~ '^[0-9a-f]{64}$'),
 target text NOT NULL CHECK(target ~ '^[0-9a-f]{64}$'),
 schema_from integer NOT NULL CHECK(schema_from BETWEEN 1 AND 65535),
 schema_to integer NOT NULL CHECK(schema_to BETWEEN schema_from AND 65535),
 revision bigint NOT NULL CHECK(revision BETWEEN 2 AND 2147483646),
 principal text NOT NULL CHECK(principal<>''),
 generation text NOT NULL CHECK(generation ~ '^[0-9a-f]{32}$'),
 record bytea NOT NULL CHECK(octet_length(record)<=5000000),
 record_hash text NOT NULL CHECK(record_hash ~ '^[0-9a-f]{64}$'),
 PRIMARY KEY(installation,sequence),
 UNIQUE(installation,digest), UNIQUE(installation,revision)
);
UPDATE extension_p1_meta SET versions_version=1;
