CREATE TABLE personal_notification_streams (
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 recipient_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 sequence bigint NOT NULL DEFAULT 0 CHECK (sequence>=0),
 PRIMARY KEY(channel_id,recipient_id)
);
CREATE TABLE personal_notifications (
 id text PRIMARY KEY DEFAULT ('pn_'||gen_random_uuid()::text),
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 recipient_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 author_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 sequence bigint NOT NULL CHECK (sequence>0),
 kind int NOT NULL CHECK (kind BETWEEN 1 AND 5),
 content_id text NOT NULL,
 reasons int NOT NULL CHECK (reasons BETWEEN 1 AND 3),
 created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
 read_at bigint,
 UNIQUE(channel_id,recipient_id,sequence),
 UNIQUE(channel_id,recipient_id,kind,content_id)
);
CREATE INDEX personal_notifications_budget ON personal_notifications(author_id,created_at) WHERE kind=5;
CREATE INDEX personal_notifications_content ON personal_notifications(channel_id,kind,content_id);

CREATE VIEW personal_notification_content AS
 SELECT 1 AS kind,t.channel_id,t.id,t.id AS parent_id,0::bigint AS sequence,t.author_id,t.author_name,
 t.title,t.body,t.mentions,''::text AS reply_recipient
 FROM forum_threads t WHERE NOT t.hidden
 UNION ALL
 SELECT 2,r.channel_id,r.id,t.id,r.sequence,r.author_id,r.author_name,t.title,r.body,r.mentions,t.author_id
 FROM forum_replies r JOIN forum_threads t ON t.id=r.thread_id WHERE NOT r.hidden AND NOT t.hidden
 UNION ALL
 SELECT 3,p.channel_id,p.id,p.id,0::bigint,p.author_id,p.author_name,''::text,p.body,p.mentions,''::text
 FROM feed_posts p WHERE NOT p.hidden
 UNION ALL
 SELECT 4,c.channel_id,c.id,p.id,c.sequence,c.author_id,c.author_name,''::text,c.body,c.mentions,p.author_id
 FROM feed_comments c JOIN feed_posts p ON p.id=c.post_id WHERE NOT c.hidden AND NOT p.hidden
 UNION ALL
 SELECT 5,c.channel_id,c.id,c.id,c.sequence,c.author_id,COALESCE(m.nickname,''),''::text,c.text,c.mentions,''::text
 FROM contents c JOIN channels ch ON ch.id=c.channel_id LEFT JOIN memberships m ON m.principal_id=c.author_id
 WHERE NOT c.deleted AND ch.mls_group_id IS NULL;

-- Счётчик обновляется под блокировкой до конца транзакции. Поздний commit
-- меньшего номера не может оказаться позади уже выданной верхней границы.
CREATE FUNCTION space_record_notification_v1(ch text,recipient text,author text,k int,content text,reason int,created bigint)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE next_sequence bigint;
BEGIN
 IF recipient IS NULL OR recipient='' OR recipient=author THEN RETURN; END IF;
 -- В старом тексте могло сохраниться имя уже удалённой личности.
 IF NOT EXISTS(SELECT 1 FROM principals WHERE id=recipient) THEN RETURN; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('personal-notifications:'||ch,0));
 UPDATE personal_notifications SET reasons=reasons|reason
 WHERE channel_id=ch AND recipient_id=recipient AND kind=k AND content_id=content;
 IF FOUND THEN RETURN; END IF;
 INSERT INTO personal_notification_streams(channel_id,recipient_id) VALUES(ch,recipient) ON CONFLICT DO NOTHING;
 UPDATE personal_notification_streams SET sequence=sequence+1
 WHERE channel_id=ch AND recipient_id=recipient RETURNING sequence INTO next_sequence;
 INSERT INTO personal_notifications(channel_id,recipient_id,author_id,sequence,kind,content_id,reasons,created_at)
 VALUES(ch,recipient,author,next_sequence,k,content,reason,created);
END $$;

CREATE FUNCTION space_capture_notifications_v1() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE k int; parent_author text; recipient text; created bigint;
BEGIN
 k=CASE TG_TABLE_NAME WHEN 'forum_threads' THEN 1 WHEN 'forum_replies' THEN 2
 WHEN 'feed_posts' THEN 3 WHEN 'feed_comments' THEN 4 ELSE 5 END;
 IF k=5 THEN
   IF NEW.deleted THEN RETURN NEW; END IF;
   IF EXISTS(SELECT 1 FROM channels WHERE id=NEW.channel_id AND mls_group_id IS NOT NULL) THEN RETURN NEW; END IF;
 END IF;
 created=extract(epoch FROM now())::bigint;
 FOR recipient IN SELECT DISTINCT item->>'principal_id' FROM jsonb_array_elements(NEW.mentions) item LOOP
   PERFORM space_record_notification_v1(NEW.channel_id,recipient,NEW.author_id,k,NEW.id,1,created);
 END LOOP;
 IF k=2 THEN SELECT author_id INTO parent_author FROM forum_threads WHERE id=NEW.thread_id; END IF;
 IF k=4 THEN SELECT author_id INTO parent_author FROM feed_posts WHERE id=NEW.post_id; END IF;
 PERFORM space_record_notification_v1(NEW.channel_id,parent_author,NEW.author_id,k,NEW.id,2,created);
 RETURN NEW;
END $$;

CREATE TRIGGER personal_notifications_chat AFTER INSERT OR UPDATE OF mentions ON contents
FOR EACH ROW EXECUTE FUNCTION space_capture_notifications_v1();
CREATE TRIGGER personal_notifications_forum_thread AFTER INSERT ON forum_threads
FOR EACH ROW EXECUTE FUNCTION space_capture_notifications_v1();
CREATE TRIGGER personal_notifications_forum_reply AFTER INSERT ON forum_replies
FOR EACH ROW EXECUTE FUNCTION space_capture_notifications_v1();
CREATE TRIGGER personal_notifications_feed_post AFTER INSERT ON feed_posts
FOR EACH ROW EXECUTE FUNCTION space_capture_notifications_v1();
CREATE TRIGGER personal_notifications_feed_comment AFTER INSERT ON feed_comments
FOR EACH ROW EXECUTE FUNCTION space_capture_notifications_v1();

-- Сохраняем прежний персональный список. Старые ответы остаются в сводке;
-- отдельная доставка ответов начинается после миграции, без лавины истории.
DO $$
DECLARE item record;
BEGIN
 FOR item IN SELECT m.*,COALESCE(m.forum_thread_id,m.forum_reply_id,m.feed_post_id,m.feed_comment_id) AS content,
 CASE WHEN m.forum_thread_id IS NOT NULL THEN 1 WHEN m.forum_reply_id IS NOT NULL THEN 2
 WHEN m.feed_post_id IS NOT NULL THEN 3 ELSE 4 END AS kind FROM community_mentions m ORDER BY m.id LOOP
   PERFORM space_record_notification_v1(item.channel_id,item.recipient_id,item.author_id,item.kind,item.content,1,item.created_at);
   UPDATE personal_notifications SET read_at=item.read_at
   WHERE channel_id=item.channel_id AND recipient_id=item.recipient_id AND kind=item.kind AND content_id=item.content;
 END LOOP;
 FOR item IN SELECT c.*,target.principal FROM contents c JOIN channels ch ON ch.id=c.channel_id
 CROSS JOIN LATERAL (SELECT DISTINCT value->>'principal_id' AS principal FROM jsonb_array_elements(c.mentions)) target
 WHERE NOT c.deleted AND ch.mls_group_id IS NULL ORDER BY c.channel_id,c.sequence LOOP
   PERFORM space_record_notification_v1(item.channel_id,item.principal,item.author_id,5,item.id,1,item.created_at_unix_ms/1000);
 END LOOP;
END $$;
