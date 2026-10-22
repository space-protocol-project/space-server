ALTER TABLE channels DROP CONSTRAINT channels_view_type_check;
ALTER TABLE channels ADD CONSTRAINT channels_view_type_check CHECK (view_type IN ('chat','voice_room','forum'));
ALTER TABLE channels ADD CONSTRAINT channels_forum_plain CHECK (view_type <> 'forum' OR (mls_group_id IS NULL AND NOT public_preview));
CREATE TABLE forum_threads (
 id text PRIMARY KEY,
 channel_id text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 author_id text NOT NULL REFERENCES principals(id),
 author_name text NOT NULL,
 title text NOT NULL CHECK (octet_length(title) BETWEEN 1 AND 320),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 8192),
 created_at bigint NOT NULL DEFAULT floor(extract(epoch FROM now())),
 sequence bigint NOT NULL CHECK (sequence > 0),
 revision bigint NOT NULL DEFAULT 1,
 closed boolean NOT NULL DEFAULT false,
 reply_count bigint NOT NULL DEFAULT 0 CHECK (reply_count >= 0),
 idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
 UNIQUE(channel_id, sequence), UNIQUE(channel_id, author_id, idempotency_key), UNIQUE(channel_id,id)
);
CREATE TABLE forum_replies (
 id text PRIMARY KEY,
 channel_id text NOT NULL,
 thread_id text NOT NULL,
 author_id text NOT NULL REFERENCES principals(id),
 author_name text NOT NULL,
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 8192),
 created_at bigint NOT NULL DEFAULT floor(extract(epoch FROM now())),
 sequence bigint NOT NULL CHECK (sequence > 0),
 idempotency_key text NOT NULL CHECK (octet_length(idempotency_key) BETWEEN 1 AND 128),
 FOREIGN KEY(channel_id,thread_id) REFERENCES forum_threads(channel_id,id) ON DELETE CASCADE,
 UNIQUE(thread_id,sequence), UNIQUE(channel_id,author_id,idempotency_key)
);
