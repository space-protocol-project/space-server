CREATE TABLE feed_read_state (
 post_id text NOT NULL REFERENCES feed_posts(id) ON DELETE CASCADE,
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 read_sequence bigint NOT NULL CHECK (read_sequence >= 0),
 PRIMARY KEY(post_id,principal_id)
);
CREATE INDEX feed_comments_unread ON feed_comments(post_id,sequence) WHERE NOT hidden;
