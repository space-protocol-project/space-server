ALTER TABLE channels DROP CONSTRAINT channels_view_type_check;
ALTER TABLE channels ADD CONSTRAINT channels_view_type_check CHECK(view_type IN ('chat','voice_room','forum','feed'));
ALTER TABLE channels ADD CONSTRAINT channels_feed_plain CHECK(view_type <> 'feed' OR (mls_group_id IS NULL AND NOT public_preview));

CREATE TABLE feed_posts (
 id text PRIMARY KEY,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 author_id text NOT NULL REFERENCES principals(id),
 author_name text NOT NULL,
 body text NOT NULL,
 created_at bigint NOT NULL DEFAULT floor(extract(epoch FROM now())),
 sequence bigint NOT NULL CHECK(sequence>0),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 comment_count bigint NOT NULL DEFAULT 0 CHECK(comment_count>=0),
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 attachments jsonb NOT NULL DEFAULT '[]',
 hidden boolean NOT NULL DEFAULT false,
 CHECK(octet_length(body)<=8192 AND (octet_length(body)>0 OR jsonb_array_length(attachments)>0)),
 CHECK(jsonb_array_length(attachments)<=5),
 UNIQUE(channel_id,id), UNIQUE(channel_id,sequence), UNIQUE(channel_id,author_id,idempotency_key)
);
CREATE TABLE feed_comments (
 id text PRIMARY KEY,
 channel_id text NOT NULL,
 post_id text NOT NULL,
 author_id text NOT NULL REFERENCES principals(id),
 author_name text NOT NULL,
 body text NOT NULL,
 created_at bigint NOT NULL DEFAULT floor(extract(epoch FROM now())),
 sequence bigint NOT NULL CHECK(sequence>0),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 attachments jsonb NOT NULL DEFAULT '[]',
 hidden boolean NOT NULL DEFAULT false,
 CHECK(octet_length(body)<=8192 AND (octet_length(body)>0 OR jsonb_array_length(attachments)>0)),
 CHECK(jsonb_array_length(attachments)<=5),
 FOREIGN KEY(channel_id,post_id) REFERENCES feed_posts(channel_id,id) ON DELETE CASCADE,
 UNIQUE(channel_id,id), UNIQUE(post_id,sequence), UNIQUE(channel_id,author_id,idempotency_key)
);
CREATE TABLE feed_access (
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 subject text NOT NULL,
 role text CHECK(role IN ('member','reader')),
 principal_id text REFERENCES principals(id) ON DELETE CASCADE,
 can_publish boolean NOT NULL DEFAULT false,
 can_comment boolean NOT NULL DEFAULT false,
 CHECK(num_nonnulls(role,principal_id)=1),
 PRIMARY KEY(channel_id,subject)
);
CREATE TABLE feed_reports (
 id bigserial PRIMARY KEY,
 post_id text NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
 comment_id text NOT NULL DEFAULT '',
 reporter_id text NOT NULL,
 reason text NOT NULL CHECK(octet_length(reason) BETWEEN 1 AND 1024),
 decision text NOT NULL DEFAULT '' CHECK(decision IN ('','dismiss','hide')),
 decision_reason text NOT NULL DEFAULT '',
 moderator_id text NOT NULL DEFAULT '',
 created_at_unix bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
 resolved_at_unix bigint NOT NULL DEFAULT 0,
 UNIQUE(post_id,comment_id,reporter_id)
);
CREATE INDEX feed_reports_page ON feed_reports(decision,id);
CREATE TABLE feed_moderation (
 id bigserial PRIMARY KEY,
 post_id text NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
 comment_id text NOT NULL DEFAULT '',
 action text NOT NULL,
 reason text NOT NULL CHECK(octet_length(reason) BETWEEN 1 AND 1024),
 actor_id text NOT NULL,
 revision bigint NOT NULL,
 created_at_unix bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
 report_id bigint NOT NULL DEFAULT 0
);
CREATE INDEX feed_moderation_page ON feed_moderation(post_id,id);
ALTER TABLE assets ADD COLUMN feed_post_id text, ADD COLUMN feed_comment_id text;
ALTER TABLE assets ADD FOREIGN KEY(channel_id,feed_post_id) REFERENCES feed_posts(channel_id,id) ON DELETE CASCADE;
ALTER TABLE assets ADD FOREIGN KEY(channel_id,feed_comment_id) REFERENCES feed_comments(channel_id,id) ON DELETE CASCADE;
ALTER TABLE assets ADD CONSTRAINT assets_single_content CHECK(num_nonnulls(content_id,mls_sequence,forum_thread_id,forum_reply_id,feed_post_id,feed_comment_id)<=1);
