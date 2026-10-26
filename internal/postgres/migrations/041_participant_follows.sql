CREATE TABLE participant_follow_versions (
 principal_id text PRIMARY KEY REFERENCES principals(id) ON DELETE CASCADE,
 revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0)
);
CREATE TABLE participant_follows (
 principal_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 target_id text NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
 CHECK(principal_id<>target_id),
 PRIMARY KEY(principal_id,target_id)
);
CREATE INDEX feed_posts_personal_page ON feed_posts(author_id,created_at DESC,channel_id COLLATE "C" DESC,id COLLATE "C" DESC) WHERE NOT hidden;
CREATE INDEX feed_posts_follow_author ON feed_posts(author_id,channel_id) WHERE NOT hidden;
CREATE INDEX feed_comments_follow_author ON feed_comments(author_id,channel_id) WHERE NOT hidden;
