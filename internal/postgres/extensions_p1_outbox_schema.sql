-- Отдельно включаемый прототип одного доверенного обработчика на instance.
CREATE TABLE IF NOT EXISTS extension_p1_delivery (
 installation text NOT NULL, instance text NOT NULL, consumer text NOT NULL,
 ack_revision bigint NOT NULL DEFAULT 0, ack_ordinal integer NOT NULL DEFAULT -1,
 lease_revision bigint NOT NULL DEFAULT 0, lease_ordinal integer NOT NULL DEFAULT -1,
 lease_token text NOT NULL DEFAULT '', lease_until timestamptz,
 last_ack_token text NOT NULL DEFAULT '', attempts bigint NOT NULL DEFAULT 0 CHECK(attempts>=0),
 PRIMARY KEY(installation,instance),
 FOREIGN KEY(installation,instance) REFERENCES extension_p1_instances ON DELETE CASCADE
);
