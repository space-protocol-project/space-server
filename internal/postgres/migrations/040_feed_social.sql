ALTER TABLE feed_posts ADD COLUMN repost_of text;
ALTER TABLE feed_posts ADD FOREIGN KEY(channel_id,repost_of)
 REFERENCES feed_posts(channel_id,id);
ALTER TABLE feed_posts ADD CONSTRAINT feed_posts_not_self_repost
 CHECK(repost_of IS NULL OR repost_of <> id);
ALTER TABLE feed_posts DROP CONSTRAINT feed_posts_check;
ALTER TABLE feed_posts ADD CONSTRAINT feed_posts_body
 CHECK(octet_length(body)<=8192 AND
 (octet_length(body)>0 OR jsonb_array_length(attachments)>0 OR repost_of IS NOT NULL));

ALTER TABLE feed_comments ADD CONSTRAINT feed_comments_post_identity UNIQUE(post_id,id);
ALTER TABLE feed_comments ADD COLUMN reply_to_comment_id text;
ALTER TABLE feed_comments ADD COLUMN reply_to_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE feed_comments ADD FOREIGN KEY(post_id,reply_to_comment_id)
 REFERENCES feed_comments(post_id,id);
ALTER TABLE feed_comments ADD CONSTRAINT feed_comments_reply_order CHECK(
 (reply_to_comment_id IS NULL AND reply_to_sequence=0) OR
 (reply_to_comment_id IS NOT NULL AND reply_to_sequence>0 AND reply_to_sequence<sequence));
CREATE INDEX feed_posts_repost ON feed_posts(repost_of) WHERE repost_of IS NOT NULL;
