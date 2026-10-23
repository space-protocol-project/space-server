CREATE TABLE forum_reports (
 id bigserial PRIMARY KEY,
 thread_id text NOT NULL REFERENCES forum_threads(id) ON DELETE CASCADE,
 reply_id text NOT NULL DEFAULT '',
 reporter_id text NOT NULL,
 reason text NOT NULL CHECK(octet_length(reason) BETWEEN 1 AND 1024),
 decision text NOT NULL DEFAULT '' CHECK(decision IN ('','dismiss','hide')),
 decision_reason text NOT NULL DEFAULT '',
 moderator_id text NOT NULL DEFAULT '',
 created_at_unix bigint NOT NULL DEFAULT extract(epoch FROM now())::bigint,
 resolved_at_unix bigint NOT NULL DEFAULT 0,
 UNIQUE(thread_id,reply_id,reporter_id)
);
CREATE INDEX forum_reports_page ON forum_reports(decision,id);
ALTER TABLE forum_moderation ADD COLUMN report_id bigint NOT NULL DEFAULT 0;
