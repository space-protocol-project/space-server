ALTER TABLE channels
    ADD COLUMN title text NOT NULL DEFAULT 'Чат' CHECK (octet_length(title) BETWEEN 1 AND 320),
    ADD COLUMN view_type text NOT NULL DEFAULT 'chat' CHECK (view_type = 'chat'),
    ADD COLUMN position integer NOT NULL DEFAULT 0 CHECK (position BETWEEN 0 AND 100000),
    ADD COLUMN archived boolean NOT NULL DEFAULT false,
    ADD COLUMN public_preview boolean NOT NULL DEFAULT false,
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);
UPDATE channels SET title = s.chat_title, public_preview = true
FROM space_settings s WHERE channels.id = 'general';

CREATE TABLE channel_access (
    channel_id text NOT NULL REFERENCES channels(id),
    subject text NOT NULL,
    role text,
    principal_id text REFERENCES principals(id),
    visible boolean NOT NULL,
    can_read boolean NOT NULL,
    can_write boolean NOT NULL,
    can_manage boolean NOT NULL,
    PRIMARY KEY (channel_id, subject),
    CHECK (num_nonnulls(role, principal_id) = 1),
    CHECK ((role IN ('member','reader') AND principal_id IS NULL AND subject = 'role:' || role)
        OR (role IS NULL AND principal_id IS NOT NULL AND subject = 'principal:' || principal_id)),
    CHECK (NOT can_write OR can_read),
    CHECK (NOT can_read OR visible),
    CHECK (NOT can_manage OR visible)
);
INSERT INTO channel_access(channel_id,subject,role,visible,can_read,can_write,can_manage)
SELECT id,'role:member','member',true,true,true,false FROM channels;
INSERT INTO channel_access(channel_id,subject,role,visible,can_read,can_write,can_manage)
SELECT id,'role:reader','reader',true,true,false,false FROM channels;
CREATE INDEX channels_order ON channels(position,id);
