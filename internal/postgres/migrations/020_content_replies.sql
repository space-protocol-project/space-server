ALTER TABLE contents ADD COLUMN reply_to_id text;
ALTER TABLE contents ADD CONSTRAINT content_reply_id_length
    CHECK (reply_to_id IS NULL OR octet_length(reply_to_id) BETWEEN 1 AND 128);
ALTER TABLE contents ADD CONSTRAINT content_reply_same_channel
    FOREIGN KEY (channel_id, reply_to_id) REFERENCES contents(channel_id, id);
