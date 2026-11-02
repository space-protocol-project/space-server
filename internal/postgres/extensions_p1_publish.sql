ALTER TABLE extension_p1_upgrade_jobs ADD COLUMN published boolean NOT NULL DEFAULT false;
ALTER TABLE extension_p1_upgrade_jobs ADD COLUMN result_digest text NOT NULL DEFAULT '';
ALTER TABLE extension_p1_upgrade_steps ADD COLUMN wasm boolean NOT NULL DEFAULT false;
CREATE TABLE extension_p1_upgrade_artifacts (
 installation text PRIMARY KEY REFERENCES extension_p1_upgrade_jobs(installation),
 target bytea NOT NULL CHECK(octet_length(target)<=131072),
 modules bytea NOT NULL CHECK(octet_length(modules)<=3000000)
);
ALTER TABLE extension_p1_lifecycle_audit DROP CONSTRAINT extension_p1_lifecycle_audit_check;
ALTER TABLE extension_p1_lifecycle_audit ADD CONSTRAINT extension_p1_lifecycle_audit_check CHECK (
 (actor='checkpoint' AND action='checkpoint' AND principal IS NULL AND previous_state IS NULL)
 OR (actor='owner' AND principal IS NOT NULL AND principal<>'' AND previous_state IS NOT NULL AND (
  (action='enable' AND previous_state='disabled' AND state='enabled') OR
  (action='disable' AND previous_state='enabled' AND state='disabling') OR
  (action='publish_upgrade' AND previous_state='disabled' AND state='disabled') OR
  (action='remove' AND previous_state IN ('disabled','quarantined') AND state='removed')))
 OR (actor='system' AND principal IS NULL AND previous_state IS NOT NULL AND (
  (action='quarantine' AND previous_state IN ('disabled','enabled','disabling') AND state='quarantined') OR
  (action='finish_disable' AND previous_state='disabling' AND state='disabled')))
);
UPDATE extension_p1_meta SET publish_version=1;
