ALTER TABLE forum_threads ADD COLUMN mentions jsonb NOT NULL DEFAULT '[]';
ALTER TABLE forum_replies ADD COLUMN mentions jsonb NOT NULL DEFAULT '[]';
ALTER TABLE feed_posts ADD COLUMN mentions jsonb NOT NULL DEFAULT '[]';
ALTER TABLE feed_comments ADD COLUMN mentions jsonb NOT NULL DEFAULT '[]';

CREATE TABLE community_mentions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 author_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 recipient_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 forum_thread_id text REFERENCES forum_threads(id) ON DELETE CASCADE,
 forum_reply_id text REFERENCES forum_replies(id) ON DELETE CASCADE,
 feed_post_id text REFERENCES feed_posts(id) ON DELETE CASCADE,
 feed_comment_id text REFERENCES feed_comments(id) ON DELETE CASCADE,
 created_at bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
 read_at bigint,
 CHECK (num_nonnulls(forum_thread_id,forum_reply_id,feed_post_id,feed_comment_id)=1),
 UNIQUE(recipient_id,forum_thread_id), UNIQUE(recipient_id,forum_reply_id),
 UNIQUE(recipient_id,feed_post_id), UNIQUE(recipient_id,feed_comment_id)
);
CREATE INDEX community_mentions_unread ON community_mentions(recipient_id,channel_id,id DESC) WHERE read_at IS NULL;
CREATE INDEX community_mentions_budget ON community_mentions(author_id,created_at);

CREATE VIEW community_mention_content AS
 SELECT 1 AS kind,t.id,t.channel_id,t.id AS parent_id,t.title,t.body,t.author_id,t.author_name,t.created_at,0::bigint AS sequence
 FROM forum_threads t WHERE NOT t.hidden
 UNION ALL
 SELECT 2,r.id,t.channel_id,t.id,t.title,r.body,r.author_id,r.author_name,r.created_at,r.sequence
 FROM forum_replies r JOIN forum_threads t ON t.id=r.thread_id WHERE NOT r.hidden AND NOT t.hidden
 UNION ALL
 SELECT 3,p.id,p.channel_id,p.id,'' AS title,p.body,p.author_id,p.author_name,p.created_at,0::bigint AS sequence
 FROM feed_posts p WHERE NOT p.hidden
 UNION ALL
 SELECT 4,c.id,p.channel_id,p.id,'' AS title,c.body,c.author_id,c.author_name,c.created_at,c.sequence
 FROM feed_comments c JOIN feed_posts p ON p.id=c.post_id WHERE NOT c.hidden AND NOT p.hidden;
