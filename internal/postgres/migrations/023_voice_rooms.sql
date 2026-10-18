ALTER TABLE channels DROP CONSTRAINT channels_view_type_check;
ALTER TABLE channels ADD CONSTRAINT channels_view_type_check CHECK (view_type IN ('chat','voice_room'));
ALTER TABLE channels ADD CONSTRAINT channels_voice_plain CHECK (view_type <> 'voice_room' OR (mls_group_id IS NULL AND NOT public_preview));
