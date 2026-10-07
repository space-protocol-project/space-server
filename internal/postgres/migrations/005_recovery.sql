ALTER TABLE device_grants ADD COLUMN parent_grant_id text REFERENCES device_grants(id);
ALTER TABLE device_grants ADD COLUMN signature_kind text NOT NULL DEFAULT 'root' CHECK (signature_kind IN ('root','recovery'));
ALTER TABLE auth_challenges DROP CONSTRAINT auth_challenges_purpose_check;
ALTER TABLE auth_challenges ADD CHECK (purpose IN ('device.register','auth.login','device.revoke','device.delegate','recovery.device.revoke'));
CREATE INDEX device_grants_parent ON device_grants(parent_grant_id);
