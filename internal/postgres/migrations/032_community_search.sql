ALTER TABLE forum_threads ADD COLUMN search_text text GENERATED ALWAYS AS (space_search_fold_v1(title || E'\n' || body)) STORED;
ALTER TABLE forum_replies ADD COLUMN search_text text GENERATED ALWAYS AS (space_search_fold_v1(body)) STORED;
ALTER TABLE feed_posts ADD COLUMN search_text text GENERATED ALWAYS AS (space_search_fold_v1(body)) STORED;
ALTER TABLE feed_comments ADD COLUMN search_text text GENERATED ALWAYS AS (space_search_fold_v1(body)) STORED;

CREATE INDEX forum_threads_search_terms ON forum_threads USING gin (space_search_terms_v1(search_text,2,3)) WHERE NOT hidden;
CREATE INDEX forum_replies_search_terms ON forum_replies USING gin (space_search_terms_v1(search_text,2,3)) WHERE NOT hidden;
CREATE INDEX feed_posts_search_terms ON feed_posts USING gin (space_search_terms_v1(search_text,2,3)) WHERE NOT hidden;
CREATE INDEX feed_comments_search_terms ON feed_comments USING gin (space_search_terms_v1(search_text,2,3)) WHERE NOT hidden;
