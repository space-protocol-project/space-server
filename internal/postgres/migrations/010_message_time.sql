-- Старые сообщения сохраняют неизвестное время, без выдуманного backfill.
ALTER TABLE contents ADD COLUMN created_at_unix_ms bigint NOT NULL DEFAULT 0;
ALTER TABLE contents ALTER COLUMN created_at_unix_ms SET DEFAULT (floor(extract(epoch FROM clock_timestamp()) * 1000)::bigint);
