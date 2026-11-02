-- Подготовка результата отдельно от рабочих данных; публикации пакета здесь нет.
CREATE TABLE extension_p1_upgrade_jobs (
 installation text PRIMARY KEY REFERENCES extension_p1_installations(installation),
 instance text NOT NULL,
 digest text NOT NULL CHECK(digest ~ '^[0-9a-f]{64}$'),
 plan bytea NOT NULL CHECK(octet_length(plan)<=16384),
 seed bytea NOT NULL CHECK(octet_length(seed)<=16384),
 document bytea NOT NULL CHECK(octet_length(document)<=16384),
 next_step integer NOT NULL CHECK(next_step BETWEEN 0 AND 32),
 principal text NOT NULL,
 FOREIGN KEY(installation,instance) REFERENCES extension_p1_instances(installation,instance)
);
CREATE TABLE extension_p1_upgrade_steps (
 installation text NOT NULL REFERENCES extension_p1_upgrade_jobs(installation),
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 31),
 step_digest text NOT NULL CHECK(step_digest ~ '^[0-9a-f]{64}$'),
 input_digest text NOT NULL CHECK(input_digest ~ '^[0-9a-f]{64}$'),
 output bytea NOT NULL CHECK(octet_length(output)<=16384),
 generation text NOT NULL CHECK(generation ~ '^[0-9a-f]{32}$'),
 principal text NOT NULL,
 PRIMARY KEY(installation,ordinal)
);
UPDATE extension_p1_meta SET upgrade_version=1;
