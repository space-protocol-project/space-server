ALTER TABLE forum_threads ADD COLUMN hidden boolean NOT NULL DEFAULT false;
ALTER TABLE forum_replies ADD COLUMN hidden boolean NOT NULL DEFAULT false;
ALTER TABLE forum_replies ADD COLUMN revision bigint NOT NULL DEFAULT 1;
CREATE TABLE forum_moderation (
 id bigserial PRIMARY KEY,
 thread_id text NOT NULL REFERENCES forum_threads(id) ON DELETE CASCADE,
 reply_id text NOT NULL DEFAULT '',
 action text NOT NULL,
 reason text NOT NULL CHECK (octet_length(reason) BETWEEN 1 AND 1024),
 actor_id text NOT NULL,
 revision bigint NOT NULL,
 created_at_unix bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint
);
CREATE INDEX forum_moderation_page ON forum_moderation(thread_id,id);
