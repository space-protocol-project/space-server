ALTER TABLE forum_threads ADD COLUMN pinned boolean NOT NULL DEFAULT false;
CREATE INDEX forum_threads_pinned_page ON forum_threads(channel_id,pinned DESC,sequence);
CREATE TABLE forum_read_state (
 thread_id text NOT NULL REFERENCES forum_threads(id) ON DELETE CASCADE,
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 read_sequence bigint NOT NULL CHECK (read_sequence >= 0),
 PRIMARY KEY(thread_id,principal_id)
);
