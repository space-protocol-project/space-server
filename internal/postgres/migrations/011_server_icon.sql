ALTER TABLE space_settings ADD COLUMN icon_preset text NOT NULL DEFAULT 'orbit';
ALTER TABLE space_settings ADD COLUMN icon_media_type text NOT NULL DEFAULT '';
ALTER TABLE space_settings ADD COLUMN icon_data bytea NOT NULL DEFAULT '\x' CHECK(octet_length(icon_data)<=8192);
