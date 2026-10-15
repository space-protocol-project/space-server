-- Идентификатор leaf не меняется при ротации ключей входа устройства.
CREATE TABLE mls_device_routes (
    leaf_grant_id text PRIMARY KEY REFERENCES mls_devices(grant_id),
    current_grant_id text NOT NULL UNIQUE REFERENCES device_grants(id)
);
INSERT INTO mls_device_routes SELECT grant_id,grant_id FROM mls_devices;
ALTER TABLE mls_channels ADD COLUMN manager_grant_id text REFERENCES mls_devices(grant_id);
UPDATE mls_channels c SET manager_grant_id=(SELECT grant_id FROM mls_members m WHERE m.channel_id=c.channel_id AND m.joined_sequence=0 LIMIT 1);
ALTER TABLE mls_channels ALTER COLUMN manager_grant_id SET NOT NULL;
