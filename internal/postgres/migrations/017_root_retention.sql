-- Подписанный список сохраняемых разрешений хранится отдельно от короткой истории root.
ALTER TABLE root_rotations ADD COLUMN retained_devices bytea NOT NULL DEFAULT '[]'::bytea;
ALTER TABLE device_grants DROP CONSTRAINT device_grants_signature_kind_check;
ALTER TABLE device_grants ADD CHECK(signature_kind IN ('root','recovery','rotation','retained'));
