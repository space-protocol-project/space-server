ALTER TABLE auth_sessions ADD COLUMN push_only boolean NOT NULL DEFAULT false;
CREATE TABLE push_registrations (
 binding text PRIMARY KEY CHECK(length(binding)=32),
 grant_id text NOT NULL UNIQUE REFERENCES device_grants(id),
 token_hash bytea NOT NULL UNIQUE REFERENCES auth_sessions(token_hash) ON DELETE CASCADE,
 secret bytea NOT NULL CHECK(octet_length(secret)=32)
);
CREATE TABLE push_queue (
 binding text NOT NULL REFERENCES push_registrations(binding) ON DELETE CASCADE,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 sequence bigint NOT NULL CHECK(sequence>0),
 event text NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(binding,channel_id)
);
-- Одна запись на канал/устройство; запись атомарна с самим сообщением.
CREATE FUNCTION enqueue_private_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO push_queue(binding,channel_id,sequence,event,expires_at)
 SELECT r.binding,NEW.channel_id,NEW.sequence,
 md5(r.binding || NEW.channel_id || NEW.sequence::text || TG_TABLE_NAME),now()+interval '1 day'
 FROM push_registrations r JOIN device_grants g ON g.id=r.grant_id
 WHERE g.revoked_at IS NULL AND g.expires_at>now()
 ON CONFLICT(binding,channel_id) DO UPDATE SET sequence=EXCLUDED.sequence,event=EXCLUDED.event,expires_at=EXCLUDED.expires_at;
 RETURN NEW;
END $$;
CREATE TRIGGER ordinary_private_push AFTER INSERT ON contents FOR EACH ROW EXECUTE FUNCTION enqueue_private_push();
ALTER TABLE mls_envelopes ADD COLUMN push_application boolean NOT NULL DEFAULT false;
CREATE TRIGGER mls_private_push AFTER INSERT ON mls_envelopes FOR EACH ROW WHEN (NEW.push_application) EXECUTE FUNCTION enqueue_private_push();
