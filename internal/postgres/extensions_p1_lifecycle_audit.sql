CREATE TABLE extension_p1_lifecycle_audit (
 installation text NOT NULL REFERENCES extension_p1_installations(installation),
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 2147483646),
 generation text NOT NULL CHECK(generation ~ '^[0-9a-f]{32}$'),
 action text NOT NULL,
 actor text NOT NULL,
 principal text,
 previous_state text,
 state text NOT NULL CHECK(state IN ('disabled','enabled','disabling','quarantined','removed')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(installation,revision),
 CHECK (
  (actor='checkpoint' AND action='checkpoint' AND principal IS NULL AND previous_state IS NULL)
  OR (actor='owner' AND principal IS NOT NULL AND principal<>'' AND previous_state IS NOT NULL AND (
   (action='enable' AND previous_state='disabled' AND state='enabled') OR
   (action='disable' AND previous_state='enabled' AND state='disabling') OR
   (action='remove' AND previous_state IN ('disabled','quarantined') AND state='removed')))
  OR (actor='system' AND principal IS NULL AND previous_state IS NOT NULL AND (
   (action='quarantine' AND previous_state IN ('disabled','enabled','disabling') AND state='quarantined') OR
   (action='finish_disable' AND previous_state='disabling' AND state='disabled')))
 )
);
-- Старые переходы не восстанавливаются вымышленными событиями.
INSERT INTO extension_p1_lifecycle_audit(installation,revision,generation,action,actor,state)
 SELECT installation,revision,generation,'checkpoint','checkpoint',state FROM extension_p1_installations;
ALTER TABLE extension_p1_meta DROP CONSTRAINT extension_p1_meta_lifecycle_version_check;
ALTER TABLE extension_p1_meta ADD CONSTRAINT extension_p1_meta_lifecycle_version_check CHECK(lifecycle_version IN (0,1,2));
UPDATE extension_p1_meta SET lifecycle_version=2;
